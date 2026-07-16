#!/usr/bin/env node
// Atryum command hook for agents that expose SessionStart and
// PreToolUse/PostToolUse style hooks.
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
// Subagents (e.g. Claude Code's Task tool spawning Explore/Plan-style workers)
// share their parent's top-level session/thread id but carry their own
// host-native subagent instance id on the same hook event. That instance id has
// nothing to do with Atryum's own `agent_id` agent-binding concept below — it
// identifies one subagent run within a session, not an authenticated caller —
// so this hook folds it into `client_session_id` as `<session-id>:<instance-id>`
// when present, giving each subagent its own get-or-create key distinct from
// its parent and from sibling subagents. Main-session hook calls, and hosts
// that never send a subagent instance id, are unaffected: `client_session_id`
// is just the session id, exactly as before.
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
// Per-message cap when trimming chat transcript entries.
const MAX_MESSAGE_CHARS = 2000;
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
const TOKEN_COMMAND_TIMEOUT_MS = envMs(
  "ATRYUM_TOKEN_COMMAND_TIMEOUT_MS",
  10000,
);
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
let planSupport;

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

function isSessionStart(event) {
  return /sessionstart/i.test(eventName(event));
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

function agentRulesURL(tool) {
  const url = new URL("/api/v1/agent/rules", API);
  url.searchParams.set("source", SOURCE);
  if (tool) url.searchParams.set("tool", tool);
  if (AGENT_ID) url.searchParams.set("agent_id", AGENT_ID);
  return url.toString();
}

async function loadAgentRules(tool) {
  const res = await fetch(agentRulesURL(tool), {
    headers: await atryumHeaders(),
  });
  if (!res.ok) return undefined;
  return res.json();
}

async function planHint(tool) {
  planSupport ||= loadAgentRules(tool).catch(() => undefined);
  const rules = await planSupport;
  if (!rules?.plan_submission?.enabled) return "";
  // The plan's actions are scoped to their source and only match later tool
  // calls from the same source, so the submission endpoint must carry it.
  let endpoint = rules.plan_submission.endpoint || "/api/v1/external/plans";
  if (!endpoint.includes("source=")) {
    endpoint +=
      (endpoint.includes("?") ? "&" : "?") +
      `source=${encodeURIComponent(SOURCE)}`;
  }
  return ` Atryum supports preapproval plans. If the user asks you to submit a plan to Atryum, you must submit a batch plan to ${endpoint} before running any of its actions and wait for approval before executing them. Also use a plan proactively for risky work or dependent changes that could leave files, systems, or external state inconsistent if a later call is denied. Keep the endpoint's source parameter: it scopes the plan's actions to this harness so later tool calls match. Give repeated actions using the same tool and server precise, distinct descriptions and input summaries: Atryum's adherence judge must identify exactly one matching action or the call requires human review. Once the plan is approved, matching tool calls are checked against it by an adherence judge (off-plan calls are denied); a plain poll of the plan's own status URL is always allowed.`;
}

function normalizeRole(value) {
  const role = String(value || "").toLowerCase();
  if (role === "human") return "user";
  if (role === "ai") return "assistant";
  if (role === "user" || role === "assistant" || role === "system") return role;
  return "";
}

function trimMessage(text) {
  const compact = String(text || "")
    .replace(/\s+\n/g, "\n")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
  if (compact.length <= MAX_MESSAGE_CHARS) return compact;
  return `${compact.slice(0, MAX_MESSAGE_CHARS)}...`;
}

function extractText(value) {
  if (typeof value === "string") return value;
  if (!value || typeof value !== "object") return "";

  if (Array.isArray(value)) {
    return value.map(extractText).filter(Boolean).join("\n");
  }

  const record = value;
  if (typeof record.text === "string") return record.text;

  const type = typeof record.type === "string" ? record.type : "";
  if (type === "tool_use" || type === "tool-call") {
    const name = typeof record.name === "string" ? record.name : "tool";
    return `[tool call: ${name}]`;
  }
  if (type === "tool_result" || type === "tool-result") {
    const status =
      typeof record.status === "string" ? record.status : "completed";
    return `[tool result: ${status}]`;
  }
  if (record.content !== undefined) return extractText(record.content);
  if (record.message !== undefined) return extractText(record.message);
  return "";
}

function messageFromRecord(record) {
  if (!record || typeof record !== "object") return undefined;

  const codex = messageFromCodexRecord(record);
  if (codex) return codex;

  const nested =
    record.message && typeof record.message === "object"
      ? record.message
      : undefined;
  const role = normalizeRole(
    nested?.role ||
      record.role ||
      record.type ||
      record.sender ||
      record.author,
  );
  if (!role) return undefined;

  const text = trimMessage(
    extractText(nested?.content ?? record.content ?? nested ?? record),
  );
  if (!text) return undefined;
  return { role, text };
}

function messageFromCodexRecord(record) {
  if (record.type === "response_item" && record.payload?.type === "message") {
    const role = normalizeRole(record.payload.role);
    const text = trimMessage(extractText(record.payload.content));
    return role && text ? { role, text } : undefined;
  }

  if (record.type === "event_msg" && record.payload?.type === "user_message") {
    const text = trimMessage(record.payload.message);
    return text ? { role: "user", text } : undefined;
  }

  if (record.type === "event_msg" && record.payload?.type === "agent_message") {
    const text = trimMessage(record.payload.message);
    return text ? { role: "assistant", text } : undefined;
  }

  if (record.type === "event_msg" && record.payload?.type === "task_complete") {
    const text = trimMessage(record.payload.last_agent_message);
    return text ? { role: "assistant", text } : undefined;
  }

  return undefined;
}

function parseChatMessages(raw) {
  const trimmed = raw.trim();
  if (!trimmed) return [];

  if (trimmed.startsWith("[") || trimmed.startsWith("{")) {
    try {
      const parsed = JSON.parse(trimmed);
      const source = Array.isArray(parsed)
        ? parsed
        : Array.isArray(parsed.messages)
          ? parsed.messages
          : Array.isArray(parsed.entries)
            ? parsed.entries
            : [];
      if (source.length > 0) {
        return source.map(messageFromRecord).filter(Boolean);
      }
      const message = messageFromRecord(parsed);
      return message ? [message] : [];
    } catch {
      // Fall through to JSONL parsing below.
    }
  }

  const messages = [];
  for (const line of raw.split(/\r?\n/)) {
    const trimmedLine = line.trim();
    if (!trimmedLine) continue;
    try {
      const message = messageFromRecord(JSON.parse(trimmedLine));
      if (message) messages.push(message);
    } catch {
      // Ignore malformed or non-message transcript lines.
    }
  }
  return messages;
}

function chatMessagesFromValue(value) {
  if (typeof value === "string") return parseChatMessages(value);
  if (!value || typeof value !== "object") return [];
  if (Array.isArray(value)) return value.map(messageFromRecord).filter(Boolean);

  const source = Array.isArray(value.messages)
    ? value.messages
    : Array.isArray(value.entries)
      ? value.entries
      : [];
  if (source.length > 0) return source.map(messageFromRecord).filter(Boolean);

  const message = messageFromRecord(value);
  return message ? [message] : [];
}

function chatMessagesFromEvent(event) {
  for (const value of [
    event.chat_messages,
    event.chatMessages,
    event.messages,
    event.conversation,
    event.transcript,
    event.chat_history,
    event.chatHistory,
    event.history,
  ]) {
    const messages = chatMessagesFromValue(value);
    if (messages.length > 0) return messages;
  }
  return [];
}

function chatHistoryPath(event) {
  return (
    process.env.ATRYUM_CHAT_HISTORY_PATH ||
    process.env.ATRYUM_CLAUDE_TRANSCRIPT_PATH ||
    process.env.ATRYUM_CURSOR_TRANSCRIPT_PATH ||
    process.env.ATRYUM_CODEX_TRANSCRIPT_PATH ||
    event.transcript_path ||
    event.transcriptPath ||
    event.conversation_path ||
    event.conversationPath ||
    ""
  );
}

// A host-native subagent instance id (e.g. Claude Code's Task-tool workers),
// present on hook events fired from inside a subagent run. This is NOT
// Atryum's `agent_id` agent-binding concept (see AGENT_ID above) — it does not
// identify a caller, it disambiguates one subagent run from its parent
// session and from sibling subagents. Hosts without a subagent concept
// (cursor, codex) simply never send this field, so callers see "".
function hostSubagentInstanceId(event) {
  return (
    event.agent_id ||
    event.agentId ||
    event.subagent_id ||
    event.subagentId ||
    ""
  );
}

// Descriptive label for which named subagent produced the event (e.g.
// "Explore", "Plan"). Observability metadata only — never part of the
// get-or-create key.
function hostSubagentType(event) {
  return event.agent_type || event.agentType || "";
}

// The host's own session/thread identifier, composed with the host-native
// subagent instance id (if any) so each subagent gets a distinct
// `client_session_id`. Sent as the invocation `thread_id` and
// `client_session_id`; Atryum resolves the internal session with
// get-or-create keyed by (agent binding, client_session_id).
function sessionId(event) {
  const base = (
    event.session_id ||
    event.sessionId ||
    event.thread_id ||
    event.threadId ||
    event.conversation_id ||
    event.conversationId ||
    ""
  );
  const subagentInstanceId = hostSubagentInstanceId(event);
  if (!subagentInstanceId) return base;
  return base ? `${base}:${subagentInstanceId}` : subagentInstanceId;
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
  // Descriptive-only: which named subagent (if any) produced this call, tacked
  // onto client_name for observability. Never part of the get-or-create key —
  // that's clientSessionID above.
  const subagentType = hostSubagentType(event);
  const clientName = subagentType ? `${CLIENT_NAME}:${subagentType}` : CLIENT_NAME;
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
        client_name: clientName,
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

function sessionStartOutput(guidance) {
  if (HOST === "cursor") return { additional_context: guidance };
  return {
    hookSpecificOutput: {
      hookEventName: "SessionStart",
      additionalContext: guidance,
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
      `atryum: tool call '${toolName(event)}' was ${decided.status} by reviewer. ${rulesEndpointHint(toolName(event))}${await planHint(toolName(event))}`,
    ),
  );
}

async function handleSessionStart() {
  const guidance = (await planHint()).trim();
  jsonOut(guidance ? sessionStartOutput(guidance) : {});
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

  if (isSessionStart(event)) {
    await handleSessionStart();
  } else if (isPreToolUse(event)) {
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
