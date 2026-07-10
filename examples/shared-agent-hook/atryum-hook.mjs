#!/usr/bin/env node
// Atryum command hook for agents that expose PreToolUse/PostToolUse style hooks.
//
// Supported host modes:
//   ATRYUM_HOOK_HOST=claude  Claude Code settings hooks
//   ATRYUM_HOOK_HOST=cursor  Cursor hooks
//   ATRYUM_HOOK_HOST=codex   Codex hooks
//
// The hook submits PreToolUse events to Atryum's external invocation API, blocks
// until the invocation is approved or denied, and reports successful PostToolUse
// results back to the same invocation.
//
// Session context for the LLM-as-judge is NOT scraped or sent by this hook. The
// harness is trusted to report which session a tool call belongs to, but it does
// not get to hand Atryum a free-form context blob (a runaway agent could use that
// to poison the judge). Instead, the hook sends the host's own session/thread id
// as `client_session_id` on every /api/v1/external/invocations submit and lets
// Atryum manage the session server-side: Atryum resolves the internal session
// with get-or-create keyed by (agent binding, client_session_id) and
// reconstructs the judge's context from the prior tool calls it recorded for
// that session, trusting tool outputs more than tool inputs and ignoring agent
// chat entirely. Because the server manages the session, this hook keeps no
// session state on disk — no mint call, no cache file, no re-mint/retry — even
// though it runs as a fresh process per tool event.
//
// A session still requires an agent binding (from ATRYUM_AGENT_ID in no-auth
// mode, or the bearer token in auth mode). When neither is present the caller is
// anonymous: the server simply resolves no session and evaluates the call
// history-free — the same graceful degradation as the session-less
// fake_agent.py baseline, now decided server-side rather than gated here.

import { createHash } from "node:crypto";
import { exec } from "node:child_process";
import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";

const execAsync = promisify(exec);

const API = process.env.ATRYUM_URL || "http://localhost:8080";
const POLL_INTERVAL = Number(process.env.ATRYUM_POLL_MS || 2000);
const RAW_HOST = (process.env.ATRYUM_HOOK_HOST || "claude").toLowerCase();
const HOST = process.env.CURSOR_INVOKED_AS ? "cursor" : RAW_HOST;
const SOURCE =
  HOST === "cursor"
    ? "cursor"
    : HOST === "codex"
      ? "codex"
      : HOST === "claude"
        ? "claude-code"
        : process.env.ATRYUM_SOURCE || process.env.ATRYUM_HOOK_HOST || "agent";
const CLIENT_NAME = process.env.ATRYUM_CLIENT_NAME || SOURCE;
const CLIENT_VERSION = process.env.ATRYUM_CLIENT_VERSION || "";
const STATE_DIR =
  process.env.ATRYUM_STATE_DIR ||
  path.join(os.homedir(), ".atryum", "agent-hook-state");
// Self-declared agent identity sent to Atryum as the invocation `agent_id`.
// When this string is listed in an Agent Record's `agent_ids` array in the
// Atryum UI, invocations from this hook get tagged to that Agent Record
// (so agent-scoped approval rules apply). Not authenticated — for verified
// identity use OAuth. Default: empty (no agent tagging).
const AGENT_ID = process.env.ATRYUM_AGENT_ID || "";
const ACCESS_TOKEN = process.env.ATRYUM_ACCESS_TOKEN || "";
const TOKEN_COMMAND = process.env.ATRYUM_TOKEN_COMMAND || "";
// Malformed values (e.g. "10s") would otherwise become NaN, which silently
// disables the cache comparisons and makes exec() throw ERR_OUT_OF_RANGE.
const envMs = (name, fallback) => {
  const n = Math.floor(Number(process.env[name] || fallback));
  return Number.isFinite(n) && n >= 0 ? n : fallback;
};
const TOKEN_REFRESH_SKEW_MS = envMs("ATRYUM_TOKEN_REFRESH_SKEW_MS", 60000);
const TOKEN_COMMAND_TIMEOUT_MS = envMs("ATRYUM_TOKEN_COMMAND_TIMEOUT_MS", 10000);
const TOKEN_CACHE_FILE = TOKEN_COMMAND
  ? path.join(STATE_DIR, "token-cache.json")
  : "";
// Ties the cached token to the command and server that produced it, so
// switching ATRYUM_TOKEN_COMMAND or ATRYUM_URL invalidates the cache instead
// of sending a token minted for a different identity or target. Trailing
// slashes are stripped so equivalent URL spellings share one cache entry.
const TOKEN_CACHE_KEY = TOKEN_COMMAND
  ? createHash("sha256")
      .update(`${TOKEN_COMMAND}\n${API.replace(/\/+$/, "")}`)
      .digest("hex")
  : "";
let cachedToken = TOKEN_COMMAND ? "" : ACCESS_TOKEN;
let cachedTokenExpiresAt =
  ACCESS_TOKEN && !TOKEN_COMMAND ? Number.POSITIVE_INFINITY : 0;

function parseTokenResponse(raw) {
  const text = String(raw || "").trim();
  if (!text) throw new Error("token command returned no token");
  if (!text.startsWith("{")) {
    if (/\s/.test(text)) {
      throw new Error("raw token command output must not contain whitespace");
    }
    return { accessToken: text, expiresAt: Date.now() + 55 * 60 * 1000 };
  }
  const parsed = JSON.parse(text);
  const accessToken =
    typeof parsed.access_token === "string"
      ? parsed.access_token
      : typeof parsed.accessToken === "string"
        ? parsed.accessToken
        : typeof parsed.token === "string"
          ? parsed.token
          : "";
  if (!accessToken) {
    throw new Error("token command response did not include access_token");
  }
  if (/\s/.test(accessToken)) {
    throw new Error("token command response token must not contain whitespace");
  }
  const toMs = (s) => (s > 1e11 ? s : s * 1000);
  // Providers send expiry fields as numbers or numeric strings; coerce, and
  // treat non-numeric or non-positive values as absent (55-minute default).
  const expiry = (v) => {
    const n = typeof v === "string" && v.trim() ? Number(v) : v;
    return typeof n === "number" && Number.isFinite(n) && n > 0 ? n : 0;
  };
  const expiresAtValue = expiry(parsed.expires_at) || expiry(parsed.expiresAt);
  const expiresIn = expiry(parsed.expires_in);
  const expiresAt = expiresAtValue
    ? toMs(expiresAtValue)
    : expiresIn
      ? Date.now() + expiresIn * 1000
      : Date.now() + 55 * 60 * 1000;
  return { accessToken, expiresAt };
}

async function readTokenCache() {
  if (!TOKEN_CACHE_FILE) return null;
  try {
    const raw = await readFile(TOKEN_CACHE_FILE, "utf8");
    const { token, expiresAt, key } = JSON.parse(raw);
    if (
      typeof token === "string" &&
      token &&
      typeof expiresAt === "number" &&
      key === TOKEN_CACHE_KEY &&
      Date.now() < expiresAt - TOKEN_REFRESH_SKEW_MS
    ) {
      return { token, expiresAt };
    }
  } catch {
    // cache miss or unreadable
  }
  return null;
}

async function writeTokenCache(token, expiresAt) {
  if (!TOKEN_CACHE_FILE) return;
  try {
    await mkdir(path.dirname(TOKEN_CACHE_FILE), { recursive: true });
    // Write to a fresh temp file so mode 0o600 applies (it is ignored on
    // existing files), then rename into place atomically.
    const tmp = `${TOKEN_CACHE_FILE}.${process.pid}.tmp`;
    await writeFile(
      tmp,
      JSON.stringify({ token, expiresAt, key: TOKEN_CACHE_KEY }),
      { encoding: "utf8", mode: 0o600 },
    );
    await rename(tmp, TOKEN_CACHE_FILE);
  } catch {
    // ignore — in-memory cache still works
  }
}

async function accessToken(forceRefresh = false) {
  if (!TOKEN_COMMAND) return ACCESS_TOKEN;
  if (!forceRefresh) {
    if (
      cachedToken &&
      Date.now() < cachedTokenExpiresAt - TOKEN_REFRESH_SKEW_MS
    ) {
      return cachedToken;
    }
    const fileCache = await readTokenCache();
    if (fileCache) {
      cachedToken = fileCache.token;
      cachedTokenExpiresAt = fileCache.expiresAt;
      return cachedToken;
    }
  }
  const { stdout } = await execAsync(TOKEN_COMMAND, {
    timeout: TOKEN_COMMAND_TIMEOUT_MS,
    maxBuffer: 1024 * 1024,
  });
  const token = parseTokenResponse(stdout);
  cachedToken = token.accessToken;
  cachedTokenExpiresAt = token.expiresAt;
  await writeTokenCache(cachedToken, cachedTokenExpiresAt);
  return cachedToken;
}

async function atryumHeaders(contentType = false, forceRefresh = false) {
  const headers = {};
  if (contentType) headers["Content-Type"] = "application/json";
  const token = await accessToken(forceRefresh);
  if (token) headers.Authorization = `Bearer ${token}`;
  return headers;
}

async function atryumFetch(url, options = {}) {
  const contentType = options.contentType === true;
  const init = { ...options };
  delete init.contentType;
  init.headers = {
    ...(await atryumHeaders(contentType)),
    ...(options.headers || {}),
  };
  let res = await fetch(url, init);
  if (res.status === 401 && TOKEN_COMMAND) {
    init.headers = {
      ...(await atryumHeaders(contentType, true)),
      ...(options.headers || {}),
    };
    res = await fetch(url, init);
  }
  return res;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function readStdin() {
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  return Buffer.concat(chunks).toString("utf8");
}

function jsonOut(value) {
  process.stdout.write(`${JSON.stringify(value)}\n`);
}

function toolName(event) {
  return (
    event.tool_name || event.toolName || event.tool || event.name || "unknown"
  );
}

function toolInput(event) {
  return event.tool_input || event.toolInput || event.input || event.args || {};
}

function toolUseID(event) {
  return (
    event.tool_use_id ||
    event.toolUseId ||
    event.toolUseID ||
    event.id ||
    event.request_id ||
    `${toolName(event)}:${createHash("sha256")
      .update(JSON.stringify(toolInput(event)))
      .digest("hex")
      .slice(0, 24)}`
  );
}

function eventName(event) {
  return String(
    process.env.ATRYUM_HOOK_EVENT ||
      event.hook_event_name ||
      event.hookEventName ||
      event.event ||
      event.type ||
      "",
  );
}

function isPreToolUse(event) {
  return /pretooluse/i.test(eventName(event));
}

function isPostToolUse(event) {
  return /posttooluse/i.test(eventName(event));
}

function describe(input) {
  const parts = Object.entries(input || {})
    .filter(([, value]) => typeof value === "string")
    .map(([key, value]) => {
      const text = String(value);
      return `${key}: ${text.length > 200 ? `${text.slice(0, 200)}...` : text}`;
    });
  return parts.join(" | ") || "(no string params)";
}

// Points the agent at the rules endpoint rather than pre-fetching and
// embedding rule content, so the model can query it directly when useful.
function rulesEndpointHint(tool) {
  const url = new URL("/api/v1/agent/rules", API);
  url.searchParams.set("server", SOURCE);
  url.searchParams.set("tool", tool);
  if (AGENT_ID && !ACCESS_TOKEN) url.searchParams.set("agent_id", AGENT_ID);
  return `atryum: to see the approval rules that apply to this call, GET ${url.toString()} (advisory only; Atryum re-checks policy during the actual gated call).`;
}

// The host's own session/thread identifier. Sent as the invocation `thread_id`
// and `client_session_id`; Atryum resolves the internal session with
// get-or-create keyed by (agent binding, client_session_id).
function sessionId(event) {
  return (
    event.session_id ||
    event.sessionId ||
    event.thread_id ||
    event.threadId ||
    event.conversation_id ||
    event.conversationId ||
    ""
  );
}

// ---------------------------------------------------------------------------
// Atryum submit (LLM-as-judge context lives server-side; the session is resolved
// by (agent binding, client_session_id) get-or-create — nothing to persist here).
// ---------------------------------------------------------------------------

async function submit(event) {
  const name = toolName(event);
  const input = toolInput(event);
  const id = toolUseID(event);
  const clientSessionID = sessionId(event) || undefined;
  const res = await atryumFetch(`${API}/api/v1/external/invocations`, {
    method: "POST",
    contentType: true,
    body: JSON.stringify({
      source: SOURCE,
      tool: name,
      description: describe(input),
      input,
      request_id: id,
      thread_id: clientSessionID,
      client_session_id: clientSessionID,
      client_name: CLIENT_NAME,
      client_version: CLIENT_VERSION || undefined,
      agent_id: AGENT_ID || undefined,
    }),
  });
  if (!res.ok) {
    throw new Error(`atryum submit failed: ${res.status} ${await res.text()}`);
  }
  return res.json();
}

async function poll(invocationId) {
  while (true) {
    const res = await atryumFetch(
      `${API}/api/v1/external/invocations/${invocationId}`,
    );
    if (!res.ok) {
      throw new Error(`atryum poll failed: ${res.status}`);
    }
    const inv = await res.json();
    if (
      inv.status !== "pending_approval" &&
      inv.status !== "received" &&
      inv.status !== "executing"
    ) {
      return inv;
    }
    await sleep(POLL_INTERVAL);
  }
}

async function patchExecution(invocationId, body) {
  const res = await atryumFetch(
    `${API}/api/v1/external/invocations/${invocationId}`,
    {
      method: "PATCH",
      contentType: true,
      body: JSON.stringify(body),
    },
  );
  if (!res.ok) {
    throw new Error(`atryum patch failed: ${res.status} ${await res.text()}`);
  }
}

function statePath(id) {
  const safe = createHash("sha256").update(id).digest("hex");
  return path.join(STATE_DIR, `${safe}.json`);
}

async function saveInvocation(toolUseId, invocationId) {
  await mkdir(STATE_DIR, { recursive: true });
  await writeFile(
    statePath(toolUseId),
    JSON.stringify({ invocation_id: invocationId }),
    "utf8",
  );
}

async function loadInvocation(toolUseId) {
  try {
    const raw = await readFile(statePath(toolUseId), "utf8");
    return JSON.parse(raw).invocation_id || "";
  } catch {
    return "";
  }
}

async function deleteInvocation(toolUseId) {
  await rm(statePath(toolUseId), { force: true });
}

function allowOutput() {
  if (HOST === "cursor") return { permission: "allow" };
  return {
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "allow",
    },
  };
}

function denyOutput(reason) {
  if (HOST === "cursor") {
    return { permission: "deny", message: reason };
  }
  return {
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "deny",
      permissionDecisionReason: reason,
    },
  };
}

function toolResult(event) {
  return (
    event.tool_response || event.toolResponse || event.output || event.result
  );
}

function isFailedResult(result) {
  if (!result || typeof result !== "object") return false;
  if (result.isError === true) return true;
  if (result.success === false) return true;
  if (result.error != null) return true;
  return false;
}

async function handlePreToolUse(event) {
  const submitted = await submit(event);
  const id = toolUseID(event);
  await saveInvocation(id, submitted.invocation_id);

  let decided = submitted;
  if (
    submitted.status === "pending_approval" ||
    submitted.status === "received" ||
    submitted.status === "executing"
  ) {
    decided = await poll(submitted.invocation_id);
  }

  if (decided.status === "approved") {
    await patchExecution(submitted.invocation_id, {
      execution_status: "running",
    });
    jsonOut(allowOutput());
    return;
  }

  await deleteInvocation(id);
  jsonOut(
    denyOutput(
      `atryum: tool call '${toolName(event)}' was ${decided.status} by reviewer. ${rulesEndpointHint(toolName(event))}`,
    ),
  );
}

async function handlePostToolUse(event) {
  const id = toolUseID(event);
  const invocationId = await loadInvocation(id);
  if (!invocationId) {
    jsonOut({});
    return;
  }
  await deleteInvocation(id);
  const status = event.tool_response_status || event.status;
  const result = toolResult(event);
  if (status === "error" || isFailedResult(result)) {
    await patchExecution(invocationId, {
      execution_status: "failed",
      error: result,
    });
  } else {
    await patchExecution(invocationId, {
      execution_status: "completed",
      result,
    });
  }
  jsonOut({});
}

async function main() {
  const raw = await readStdin();
  const event = raw.trim() ? JSON.parse(raw) : {};

  if (isPreToolUse(event)) {
    await handlePreToolUse(event);
  } else if (isPostToolUse(event)) {
    await handlePostToolUse(event);
  } else {
    jsonOut({});
  }
}

main().catch((err) => {
  jsonOut(
    denyOutput(`atryum: failed to gate tool call: ${err.message || err}`),
  );
  process.exitCode = 0;
});
