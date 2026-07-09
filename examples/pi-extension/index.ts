import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { exec } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { promisify } from "node:util";

const execAsync = promisify(exec);

const API = process.env.ATRYUM_URL || "http://localhost:8080";
const SOURCE = process.env.ATRYUM_SOURCE || "pi";
const POLL_INTERVAL = Number(process.env.ATRYUM_POLL_MS || 2000);
const CLIENT_NAME = process.env.ATRYUM_CLIENT_NAME || SOURCE;
const CLIENT_VERSION =
  process.env.ATRYUM_CLIENT_VERSION || process.env.PI_VERSION || "";
// Self-declared agent identity. Atryum resolves the Agent Record via the
// agents.agent_ids array. Not authenticated; for verified identity use OAuth.
const AGENT_ID = process.env.ATRYUM_AGENT_ID || "";
const ACCESS_TOKEN = process.env.ATRYUM_ACCESS_TOKEN || "";
const TOKEN_COMMAND = process.env.ATRYUM_TOKEN_COMMAND || "";
// Malformed values (e.g. "10s") would otherwise become NaN, which silently
// disables the cache comparisons and makes exec() throw ERR_OUT_OF_RANGE.
const envMs = (name: string, fallback: number) => {
  const n = Math.floor(Number(process.env[name] || fallback));
  return Number.isFinite(n) && n >= 0 ? n : fallback;
};
const TOKEN_REFRESH_SKEW_MS = envMs("ATRYUM_TOKEN_REFRESH_SKEW_MS", 60000);
const TOKEN_COMMAND_TIMEOUT_MS = envMs("ATRYUM_TOKEN_COMMAND_TIMEOUT_MS", 10000);
const STATE_DIR =
  process.env.ATRYUM_STATE_DIR ||
  join(homedir(), ".atryum", "pi-extension-state");
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
  approval?: {
    reason?: string;
  };
  error?: unknown;
};

type AgentRulesResponse = {
  plan_submission?: {
    enabled?: boolean;
    endpoint?: string;
    message?: string;
  };
};
type ToolInput = Record<string, unknown>;

const invocationMap = new Map<string, string>();
let planSupport: Promise<AgentRulesResponse | undefined> | undefined;

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function describe(input: ToolInput): string {
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

async function loadAgentRules(tool?: string): Promise<AgentRulesResponse | undefined> {
  const res = await fetch(agentRulesURL(tool), { headers: await atryumHeaders() });
  if (!res.ok) return undefined;
  return (await res.json()) as AgentRulesResponse;
}

async function planHint(tool?: string): Promise<string> {
  planSupport ||= loadAgentRules(tool).catch(() => undefined);
  const rules = await planSupport;
  if (!rules?.plan_submission?.enabled) return "";
  const endpoint = rules.plan_submission.endpoint || "/api/v1/external/plans";
  return ` Atryum supports preapproval plans for complex, risky, or multi-step work. For tasks with two or more tool calls, file changes, shell commands, external systems, or ordered actions, submit a batch plan to ${endpoint} before running tools, then wait for approval before executing the planned steps.`;
}

// Pi's own session identifier, sent as client_session_id (and thread_id) on
// every submission. Atryum resolves the internal session with get-or-create
// keyed by (agent binding, client_session_id) — the extension never mints,
// persists, or echoes an Atryum session id.
function piClientSessionID(ctx: unknown): string | undefined {
  const manager = (ctx as { sessionManager?: unknown }).sessionManager as
    | { getSessionFile?: () => string; sessionId?: string; id?: string }
    | undefined;
  if (!manager) return undefined;
  if (typeof manager.sessionId === "string") return manager.sessionId;
  if (typeof manager.id === "string") return manager.id;
  if (typeof manager.getSessionFile === "function") {
    return manager.getSessionFile();
  }
  return undefined;
}

async function submit(
  tool: string,
  toolCallID: string,
  input: ToolInput,
  threadID: string | undefined,
): Promise<InvocationResponse> {
  const res = await atryumFetch(`${API}/api/v1/external/invocations`, {
    method: "POST",
    contentType: true,
    body: JSON.stringify({
      source: SOURCE,
      tool,
      description: describe(input),
      input,
      request_id: toolCallID,
      thread_id: threadID,
      // Pi's own session id. Atryum resolves the internal session with
      // get-or-create keyed by (agent binding, client_session_id).
      client_session_id: threadID,
      agent_id: AGENT_ID || undefined,
      client_name: CLIENT_NAME,
      client_version: CLIENT_VERSION || undefined,
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
      throw new Error(`atryum poll failed: ${res.status} ${await res.text()}`);
    }
    const inv = (await res.json()) as InvocationResponse;
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

export default function (pi: ExtensionAPI) {
  pi.on("tool_call", async (event, ctx) => {
    try {
      const input = (event.input || {}) as ToolInput;
      const submitted = await submit(
        event.toolName,
        event.toolCallId,
        input,
        piClientSessionID(ctx),
      );
      invocationMap.set(event.toolCallId, submitted.invocation_id);

      let decided = submitted;
      if (
        submitted.status === "pending_approval" ||
        submitted.status === "received" ||
        submitted.status === "executing"
      ) {
        ctx.ui.setStatus("atryum", `awaiting approval for ${event.toolName}`);
        decided = await poll(submitted.invocation_id);
      }

      if (decided.status === "approved") {
        await patchExecution(submitted.invocation_id, {
          execution_status: "running",
        });
        ctx.ui.setStatus("atryum", `approved ${event.toolName}`);
        return;
      }

      invocationMap.delete(event.toolCallId);
      ctx.ui.setStatus("atryum", `blocked ${event.toolName}`);
      const reviewerReason = decided.approval?.reason
        ? ` Reason: ${decided.approval.reason}`
        : "";
      return {
        block: true,
        reason: `atryum: tool call '${event.toolName}' was ${decided.status} by reviewer.${reviewerReason} ${rulesEndpointHint(event.toolName)}${await planHint(event.toolName)}`,
      };
    } catch (err) {
      ctx.ui.setStatus("atryum", "gate failed");
      return {
        block: true,
        reason: `atryum: failed to gate tool call: ${err}`,
      };
    }
  });

  pi.on("tool_execution_end", async (event, ctx) => {
    const invocationID = invocationMap.get(event.toolCallId);
    if (!invocationID) return;
    invocationMap.delete(event.toolCallId);

    try {
      if (event.isError) {
        await patchExecution(invocationID, {
          execution_status: "failed",
          error: event.result,
        });
      } else {
        await patchExecution(invocationID, {
          execution_status: "completed",
          result: event.result,
        });
      }
      ctx.ui.setStatus("atryum", "");
    } catch (err) {
      ctx.ui.setStatus("atryum", `audit update failed: ${err}`);
    }
  });

  pi.on("session_shutdown", async () => {
    for (const invocationID of invocationMap.values()) {
      try {
        await patchExecution(invocationID, {
          execution_status: "cancelled",
          message: "Pi session shut down before the tool result was reported.",
        });
      } catch {
        // Best-effort shutdown audit only.
      }
    }
    invocationMap.clear();
  });
}
