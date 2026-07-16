// @i-know-the-amp-plugin-api-is-wip-and-very-experimental-right-now
//
// Atryum amp plugin
// -----------------
// Routes every amp tool.call through Atryum for human approval, then reports
// the execution outcome on tool.result. Atryum itself does not execute the
// tool — amp does. Atryum is the approval mediator and audit log.
//
// Session context for the LLM-as-judge is NOT sent by this plugin. The harness
// is trusted to report which session a tool call belongs to, but it does not
// get to hand Atryum a free-form context blob (a runaway agent could use that
// to poison the judge). Instead, the plugin sends Amp's own thread id as
// `client_session_id` on every submission and lets Atryum manage the session
// server-side: Atryum resolves the internal session with get-or-create keyed by
// (agent binding, client_session_id) and reconstructs the judge's context from
// the prior tool calls it recorded for that session, which it trusts at the
// appropriate level (tool outputs > tool inputs > nothing from agent chat). The
// plugin never mints, persists, or echoes an Atryum session id.
//
// Configure via env:
//   ATRYUM_URL       base URL of the atryum server, default http://localhost:8080
//   ATRYUM_SOURCE    label that shows up in the atryum UI as the "upstream"
//                    column, default "amp"
//   ATRYUM_POLL_MS   poll interval in ms while waiting for approval, default 2000
//   ATRYUM_AGENT_ID  stable agent identifier sent to atryum as the
//                    invocation's `agent_id`. When this string is listed in
//                    an Agent Record's `agent_ids` array in the atryum UI,
//                    invocations from this plugin will be tagged to that
//                    Agent Record (so agent-scoped approval rules apply).
//                    Default: empty (no agent tagging).
//   ATRYUM_ACCESS_TOKEN
//                    optional OAuth bearer token for Atryum's agent-runtime
//                    APIs. Required when Atryum runs with [[auth]] configured;
//                    Atryum then derives the agent identity from the token and
//                    ignores ATRYUM_AGENT_ID.
//   ATRYUM_TOKEN_COMMAND
//                    optional shell command that prints a bearer token (raw
//                    string or JSON with access_token/expires_at/expires_in).
//                    When set it is used instead of ATRYUM_ACCESS_TOKEN and the
//                    token is refreshed on expiry and on a 401.
//   ATRYUM_AMP_SESSION_FILE
//                    override Amp session JSON file, default
//                    ~/.local/share/amp/session.json. Used to derive Amp's own
//                    thread id, sent as client_session_id so Atryum can group a
//                    thread's tool calls into one server-managed session.

import type { PluginAPI } from "@ampcode/plugin";
import { exec } from "node:child_process";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { promisify } from "node:util";

const execAsync = promisify(exec);

const API = process.env.ATRYUM_URL || "http://localhost:8080";
const SOURCE = process.env.ATRYUM_SOURCE || "amp";
const POLL_INTERVAL = Number(process.env.ATRYUM_POLL_MS || 2000);
const AMP_SESSION_FILE =
  process.env.ATRYUM_AMP_SESSION_FILE ||
  join(homedir(), ".local", "share", "amp", "session.json");
// Amp has used different env names across builds; session.json is the fallback.
const ENV_THREAD_ID =
  process.env.THREAD_ID ||
  process.env.AMP_THREAD_ID ||
  process.env.AMP_CURRENT_THREAD_ID ||
  "";
// Harness identity for the Atryum invocations UI Agent column. Falls back
// to SOURCE for the name. ATRYUM_CLIENT_VERSION / AMP_VERSION are checked
// in case a deployment plumbs the build version through the env; safe to
// leave empty.
const CLIENT_NAME = process.env.ATRYUM_CLIENT_NAME || SOURCE;
const CLIENT_VERSION =
  process.env.ATRYUM_CLIENT_VERSION || process.env.AMP_VERSION || "";
// Per-message cap when trimming chat transcript entries.
const MAX_MESSAGE_CHARS = 2000;
// Self-declared agent identity. Atryum resolves the Agent Record via the
// agents.agent_ids JSON array, so any string the user has added to an
// Agent Record (e.g. "amp-local", "amp-alice", a service account id, etc.)
// will work. Not authenticated — for verified identity use OAuth.
const AGENT_ID = process.env.ATRYUM_AGENT_ID || "";
// Optional OAuth bearer token. When Atryum runs with one or more [[auth]]
// blocks, the agent-runtime APIs (sessions, invocations) require a token and
// Atryum derives the agent identity from it (ignoring ATRYUM_AGENT_ID).
const ACCESS_TOKEN = process.env.ATRYUM_ACCESS_TOKEN || "";
const TOKEN_COMMAND = process.env.ATRYUM_TOKEN_COMMAND || "";
// Malformed values (e.g. "10s") would otherwise become NaN, which silently
// disables the cache comparisons and makes exec() throw ERR_OUT_OF_RANGE.
const envMs = (name: string, fallback: number) => {
  const n = Math.floor(Number(process.env[name] || fallback));
  return Number.isFinite(n) && n >= 0 ? n : fallback;
};
const TOKEN_REFRESH_SKEW_MS = envMs("ATRYUM_TOKEN_REFRESH_SKEW_MS", 60000);
const TOKEN_COMMAND_TIMEOUT_MS = envMs(
  "ATRYUM_TOKEN_COMMAND_TIMEOUT_MS",
  10000,
);
const STATE_DIR =
  process.env.ATRYUM_STATE_DIR ||
  join(homedir(), ".atryum", "amp-plugin-state");
const TOKEN_CACHE_FILE = TOKEN_COMMAND
  ? join(STATE_DIR, "token-cache.json")
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
let refreshPromise: Promise<string> | null = null;

function parseTokenResponse(raw: string): {
  accessToken: string;
  expiresAt: number;
} {
  const text = raw.trim();
  if (!text) throw new Error("token command returned no token");
  if (!text.startsWith("{")) {
    if (/\s/.test(text)) {
      throw new Error("raw token command output must not contain whitespace");
    }
    return { accessToken: text, expiresAt: Date.now() + 55 * 60 * 1000 };
  }
  const parsed = JSON.parse(text) as Record<string, unknown>;
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
  const toMs = (s: number) => (s > 1e11 ? s : s * 1000);
  // Providers send expiry fields as numbers or numeric strings; coerce, and
  // treat non-numeric or non-positive values as absent (55-minute default).
  const expiry = (v: unknown) => {
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

async function readTokenCache(): Promise<{
  token: string;
  expiresAt: number;
} | null> {
  if (!TOKEN_CACHE_FILE) return null;
  try {
    const raw = await readFile(TOKEN_CACHE_FILE, "utf8");
    const { token, expiresAt, key } = JSON.parse(raw) as {
      token?: unknown;
      expiresAt?: unknown;
      key?: unknown;
    };
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

async function writeTokenCache(token: string, expiresAt: number) {
  if (!TOKEN_CACHE_FILE) return;
  try {
    await mkdir(dirname(TOKEN_CACHE_FILE), { recursive: true });
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

async function refreshAccessToken(useFileCache: boolean): Promise<string> {
  if (useFileCache) {
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

async function accessToken(forceRefresh = false): Promise<string> {
  if (!TOKEN_COMMAND) return ACCESS_TOKEN;
  if (
    !forceRefresh &&
    cachedToken &&
    Date.now() < cachedTokenExpiresAt - TOKEN_REFRESH_SKEW_MS
  ) {
    return cachedToken;
  }
  if (!forceRefresh && refreshPromise) return refreshPromise;
  const p = refreshAccessToken(!forceRefresh).finally(() => {
    if (refreshPromise === p) refreshPromise = null;
  });
  if (!forceRefresh) refreshPromise = p;
  return p;
}

async function atryumHeaders(
  contentType = false,
  forceRefresh = false,
): Promise<Record<string, string>> {
  const headers: Record<string, string> = {};
  if (contentType) headers["Content-Type"] = "application/json";
  const token = await accessToken(forceRefresh);
  if (token) headers.Authorization = `Bearer ${token}`;
  return headers;
}

async function atryumFetch(
  url: string,
  options: RequestInit & { contentType?: boolean } = {},
): Promise<Response> {
  const { contentType = false, ...init } = options;
  init.headers = {
    ...(await atryumHeaders(contentType)),
    ...((options.headers as Record<string, string> | undefined) || {}),
  };
  let res = await fetch(url, init);
  if (res.status === 401 && TOKEN_COMMAND) {
    init.headers = {
      ...(await atryumHeaders(contentType, true)),
      ...((options.headers as Record<string, string> | undefined) || {}),
    };
    res = await fetch(url, init);
  }
  return res;
}

type InvocationStatus =
  | "received"
  | "executing"
  | "pending_approval"
  | "approved"
  | "denied"
  | "expired"
  | "cancelled"
  | "succeeded"
  | "failed";

type InvocationResponse = {
  invocation_id: string;
  status: InvocationStatus;
  error?: unknown;
};

type AgentRulesResponse = {
  plan_submission?: {
    enabled?: boolean;
    endpoint?: string;
    message?: string;
  };
};

type ChatMessage = {
  role: string;
  text: string;
};
// toolUseID -> atryum invocation id, so tool.result can patch the right row.
const invocationMap = new Map<string, string>();
const activityContext: ChatMessage[] = [];
let planSupport: Promise<AgentRulesResponse | undefined> | undefined;

function normalizeRole(role: unknown): string | undefined {
  if (role !== "user" && role !== "assistant" && role !== "system") {
    return undefined;
  }
  return role;
}

function trimMessage(text: string): string {
  const compact = text
    .replace(/\s+\n/g, "\n")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
  if (compact.length <= MAX_MESSAGE_CHARS) return compact;
  return `${compact.slice(0, MAX_MESSAGE_CHARS)}...`;
}

function extractText(value: unknown): string {
  if (typeof value === "string") return value;
  if (!value || typeof value !== "object") return "";

  if (Array.isArray(value)) {
    return value.map(extractText).filter(Boolean).join("\n");
  }

  const record = value as Record<string, unknown>;
  if (typeof record.text === "string") return record.text;

  const type = typeof record.type === "string" ? record.type : "";
  if (type === "tool_use" || type === "tool-call") {
    const name = typeof record.name === "string" ? record.name : "tool";
    return `[tool call: ${name}]`;
  }
  if (type === "tool_result" || type === "tool-result") {
    const run = record.run as Record<string, unknown> | undefined;
    const status =
      typeof record.status === "string"
        ? record.status
        : typeof run?.status === "string"
          ? run.status
          : "completed";
    return `[tool result: ${status}]`;
  }
  if (record.content !== undefined) return extractText(record.content);
  if (record.message !== undefined) return extractText(record.message);
  return "";
}

function chatMessagesFromValue(value: unknown): ChatMessage[] {
  if (!value || typeof value !== "object") return [];

  const root = value as Record<string, unknown>;
  const source = Array.isArray(root.messages) ? root.messages : value;
  if (!Array.isArray(source)) return [];

  const messages: ChatMessage[] = [];
  for (const item of source) {
    if (!item || typeof item !== "object") continue;
    const record = item as Record<string, unknown>;
    const role = normalizeRole(record.role);
    if (!role) continue;
    const text = trimMessage(extractText(record.content ?? record.message));
    if (text) messages.push({ role, text });
  }
  return messages;
}

function chatMessagesFromContext(ctx: unknown): ChatMessage[] {
  const manager = (ctx as { sessionManager?: unknown } | undefined)
    ?.sessionManager as
    | {
        getBranch?: () => unknown;
        getThread?: () => unknown;
        getMessages?: () => unknown;
      }
    | undefined;

  for (const getter of [
    manager?.getBranch,
    manager?.getThread,
    manager?.getMessages,
  ]) {
    if (typeof getter !== "function") continue;
    try {
      const messages = chatMessagesFromValue(getter.call(manager));
      if (messages.length > 0) return messages;
    } catch {
      // Amp plugin internals are not stable; fall through to thread file.
    }
  }
  return [];
}

function activeThreadID(): string {
  if (ENV_THREAD_ID) return ENV_THREAD_ID;
  try {
    const session = JSON.parse(readFileSync(AMP_SESSION_FILE, "utf8")) as {
      lastThreadId?: unknown;
      lastThreadByTerminal?: unknown;
    };
    if (typeof session.lastThreadId === "string") return session.lastThreadId;

    const byTerminal = session.lastThreadByTerminal;
    if (byTerminal && typeof byTerminal === "object") {
      const newest = Object.values(
        byTerminal as Record<
          string,
          { updatedAt?: unknown; lastThreadId?: unknown }
        >,
      )
        .filter((entry) => typeof entry.lastThreadId === "string")
        .sort((a, b) => Number(b.updatedAt || 0) - Number(a.updatedAt || 0))[0];
      if (typeof newest?.lastThreadId === "string") return newest.lastThreadId;
    }
  } catch {
    // No readable session file; the session just won't carry Amp's thread id.
  }
  return "";
}

function describe(input: Record<string, unknown>): string {
  const parts = Object.entries(input)
    .filter(([, v]) => typeof v === "string")
    .map(([k, v]) => {
      const s = String(v);
      return `${k}: ${s.length > 200 ? s.slice(0, 200) + "..." : s}`;
    });
  return parts.join(" | ") || "(no string params)";
}

// Points the agent at the rules endpoint rather than pre-fetching and
// embedding rule content, so the model can query it directly when useful.
function rulesEndpointHint(tool: string): string {
  const url = new URL("/api/v1/agent/rules", API);
  url.searchParams.set("server", SOURCE);
  url.searchParams.set("tool", tool);
  if (AGENT_ID && !ACCESS_TOKEN) url.searchParams.set("agent_id", AGENT_ID);
  return `atryum: to see the approval rules that apply to this call, GET ${url.toString()} (advisory only; Atryum re-checks policy during the actual gated call).`;
}
function agentRulesURL(tool?: string): string {
  const url = new URL("/api/v1/agent/rules", API);
  url.searchParams.set("source", SOURCE);
  if (tool) url.searchParams.set("tool", tool);
  if (AGENT_ID) url.searchParams.set("agent_id", AGENT_ID);
  return url.toString();
}

async function loadAgentRules(
  tool?: string,
): Promise<AgentRulesResponse | undefined> {
  const res = await fetch(agentRulesURL(tool), {
    headers: await atryumHeaders(),
  });
  if (!res.ok) return undefined;
  return (await res.json()) as AgentRulesResponse;
}

async function planHint(tool?: string): Promise<string> {
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
  return ` Atryum supports preapproval plans. If the user asks you to submit a plan to Atryum, you must submit a batch plan to ${endpoint} before running any of its actions and wait for approval before executing them. Also use a plan proactively for risky work or dependent changes that could leave files, systems, or external state inconsistent if a later call is denied. Keep the endpoint's source parameter: it scopes the plan's actions to this harness so later tool calls match. Give repeated actions using the same tool and server precise, distinct descriptions and input summaries so the adherence judge can compare each call to its intended actions. Once the plan is approved, calls confirmed to follow one or more eligible actions are allowed, while off-plan calls are denied; a plain poll of the plan's own status URL is always allowed.`;
}

async function submit(
  tool: string,
  toolUseID: string,
  input: Record<string, unknown>,
): Promise<InvocationResponse> {
  const threadID = activeThreadID() || undefined;
  const res = await atryumFetch(`${API}/api/v1/external/invocations`, {
    method: "POST",
    contentType: true,
    body: JSON.stringify({
      source: SOURCE,
      tool,
      description: describe(input),
      input,
      request_id: toolUseID,
      thread_id: threadID,
      // Amp's own thread id. Atryum resolves the internal session with
      // get-or-create keyed by (agent binding, client_session_id) — no mint,
      // no persisted session id, no re-mint on expiry.
      client_session_id: threadID,
      client_name: CLIENT_NAME,
      client_version: CLIENT_VERSION || undefined,
      agent_id: AGENT_ID || undefined,
    }),
  });
  if (!res.ok) {
    throw new Error(`atryum submit failed: ${res.status} ${await res.text()}`);
  }
  return (await res.json()) as InvocationResponse;
}

async function poll(invocationID: string): Promise<InvocationResponse> {
  while (true) {
    const res = await atryumFetch(
      `${API}/api/v1/external/invocations/${invocationID}`,
      {},
    );
    if (!res.ok) {
      throw new Error(`atryum poll failed: ${res.status}`);
    }
    const inv = (await res.json()) as InvocationResponse;
    if (
      inv.status !== "pending_approval" &&
      inv.status !== "received" &&
      inv.status !== "executing"
    ) {
      return inv;
    }
    await new Promise((r) => setTimeout(r, POLL_INTERVAL));
  }
}

async function patchExecution(
  invocationID: string,
  body: {
    execution_status: "running" | "completed" | "failed" | "cancelled";
    result?: unknown;
    error?: unknown;
    message?: string;
  },
): Promise<void> {
  const res = await atryumFetch(
    `${API}/api/v1/external/invocations/${invocationID}`,
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

export default function (amp: PluginAPI) {
  // Amp's session.start event cannot add model context itself. Mark the thread
  // unguided and inject into its first agent turn instead. Tracking completed
  // threads also covers runtimes that load the plugin after session.start.
  const sessionsWithPlanGuidance = new Set<string>();

  amp.on("session.start", async (event) => {
    sessionsWithPlanGuidance.delete(event.thread.id);
  });

  amp.on("agent.start", async (event) => {
    if (sessionsWithPlanGuidance.has(event.thread.id)) return {};
    sessionsWithPlanGuidance.add(event.thread.id);
    const guidance = (await planHint()).trim();
    if (!guidance) return {};
    return {
      message: {
        content: guidance,
        display: false,
      },
    };
  });

  amp.on("tool.call", async (event, ctx) => {
    try {
      const submitted = await submit(
        event.tool,
        event.toolUseID,
        event.input,
      );
      invocationMap.set(event.toolUseID, submitted.invocation_id);

      // If rules already decided (auto_approve / auto_deny), skip polling.
      let decided = submitted;
      if (
        submitted.status === "pending_approval" ||
        submitted.status === "received" ||
        submitted.status === "executing"
      ) {
        ctx.logger.log(
          `atryum: submitted ${event.tool} as ${submitted.invocation_id} — awaiting approval`,
        );
        decided = await poll(submitted.invocation_id);
      }

      if (decided.status === "approved") {
        await patchExecution(submitted.invocation_id, {
          execution_status: "running",
        });
        ctx.logger.log(
          `atryum: approved ${event.tool} (${submitted.invocation_id})`,
        );
        return { action: "allow" };
      }
      ctx.logger.log(
        `atryum: rejected ${event.tool} (${submitted.invocation_id}, status=${decided.status})`,
      );
      invocationMap.delete(event.toolUseID);
      return {
        action: "reject-and-continue",
        message: `atryum: tool call '${event.tool}' was ${decided.status} by reviewer. ${rulesEndpointHint(event.tool)}${await planHint(event.tool)}`,
      };
    } catch (err) {
      ctx.logger.log(`atryum error: ${err}`);
      return {
        action: "reject-and-continue",
        message: `atryum: failed to gate tool call: ${err}`,
      };
    }
  });

  amp.on("tool.result", async (event, ctx) => {
    const invocationID = invocationMap.get(event.toolUseID);
    if (!invocationID) return;
    invocationMap.delete(event.toolUseID);
    try {
      if (event.status === "done") {
        await patchExecution(invocationID, {
          execution_status: "completed",
          result: event.output,
        });
      } else if (event.status === "error") {
        await patchExecution(invocationID, {
          execution_status: "failed",
          error: event.output,
        });
      } else if (event.status === "cancelled") {
        await patchExecution(invocationID, {
          execution_status: "cancelled",
        });
      }
    } catch (err) {
      ctx.logger.log(`atryum result update error: ${err}`);
    }
  });
}
