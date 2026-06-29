import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { exec } from "node:child_process";
import { readFileSync } from "node:fs";
import { promisify } from "node:util";

const execAsync = promisify(exec);

const API = process.env.ATRYUM_URL || "http://localhost:8080";
const SOURCE = process.env.ATRYUM_SOURCE || "pi";
const POLL_INTERVAL = Number(process.env.ATRYUM_POLL_MS || 2000);
const CLIENT_NAME = process.env.ATRYUM_CLIENT_NAME || SOURCE;
const CLIENT_VERSION =
  process.env.ATRYUM_CLIENT_VERSION || process.env.PI_VERSION || "";
const CHAT_MESSAGES_LIMIT = Number(
  process.env.ATRYUM_CHAT_MESSAGES_LIMIT || 100,
);
// Self-declared agent identity. Atryum resolves the Agent Record via the
// agents.agent_ids array. Not authenticated; for verified identity use OAuth.
const AGENT_ID = process.env.ATRYUM_AGENT_ID || "";
const ACCESS_TOKEN = process.env.ATRYUM_ACCESS_TOKEN || "";
const TOKEN_COMMAND = process.env.ATRYUM_TOKEN_COMMAND || "";
const TOKEN_REFRESH_SKEW_MS = Number(
  process.env.ATRYUM_TOKEN_REFRESH_SKEW_MS || 60000,
);
let cachedToken = ACCESS_TOKEN;
let cachedTokenExpiresAt = ACCESS_TOKEN && !TOKEN_COMMAND ? Number.POSITIVE_INFINITY : 0;
let refreshPromise: Promise<string> | null = null;

function parseTokenResponse(raw: string): {
  accessToken: string;
  expiresAt: number;
} {
  const text = raw.trim();
  if (!text) throw new Error("token command returned no token");
  if (!text.startsWith("{")) {
    return { accessToken: text, expiresAt: Date.now() + 55 * 60 * 1000 };
  }
  const parsed = JSON.parse(text) as Record<string, unknown>;
  const accessToken = firstString(parsed, [
    "access_token",
    "accessToken",
    "token",
  ]);
  if (!accessToken) {
    throw new Error("token command response did not include access_token");
  }
  const toMs = (s: number) => (s > 1e11 ? s : s * 1000);
  const expiresAt =
    typeof parsed.expires_at === "number"
      ? toMs(parsed.expires_at)
      : typeof parsed.expiresAt === "number"
        ? toMs(parsed.expiresAt)
        : typeof parsed.expires_in === "number"
          ? Date.now() + parsed.expires_in * 1000
          : Date.now() + 55 * 60 * 1000;
  return { accessToken, expiresAt };
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
  const p = execAsync(TOKEN_COMMAND, {
    timeout: Number(process.env.ATRYUM_TOKEN_COMMAND_TIMEOUT_MS || 10000),
    maxBuffer: 1024 * 1024,
  })
    .then(({ stdout }) => {
      const token = parseTokenResponse(stdout);
      cachedToken = token.accessToken;
      cachedTokenExpiresAt = token.expiresAt;
      return cachedToken;
    })
    .finally(() => {
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

type ToolInput = Record<string, unknown>;
type ChatMessage = { role: string; text: string };

const invocationMap = new Map<string, string>();

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

function sessionID(ctx: unknown): string | undefined {
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

function sessionFile(ctx: unknown): string | undefined {
  const manager = (ctx as { sessionManager?: unknown }).sessionManager as
    { getSessionFile?: () => string } | undefined;
  if (!manager || typeof manager.getSessionFile !== "function") {
    return undefined;
  }
  const file = manager.getSessionFile();
  if (typeof file === "string" && file !== "") {
    return file;
  }
  return undefined;
}

function chatContext(
  ctx: unknown,
): { context: string; count: number } | undefined {
  if (CHAT_MESSAGES_LIMIT <= 0) return undefined;
  const messages = recentChatMessages(ctx, CHAT_MESSAGES_LIMIT);
  if (messages.length === 0) return undefined;
  return {
    count: messages.length,
    context: [
      `Recent Pi chat messages (oldest to newest, up to ${CHAT_MESSAGES_LIMIT}):`,
      ...messages.map((msg) => `- ${msg.role}: ${msg.text}`),
    ].join("\n"),
  };
}

function recentChatMessages(ctx: unknown, limit: number): ChatMessage[] {
  const fromSession = recentChatMessagesFromSessionManager(ctx, limit);
  if (fromSession.length > 0) return fromSession;

  const file = sessionFile(ctx);
  if (!file) return [];
  return recentChatMessagesFromFile(file, limit);
}

function recentChatMessagesFromSessionManager(
  ctx: unknown,
  limit: number,
): ChatMessage[] {
  const manager = (ctx as { sessionManager?: unknown }).sessionManager as
    | {
        getBranch?: () => unknown[];
        getEntries?: () => unknown[];
      }
    | undefined;
  const entries = manager?.getBranch?.() ?? manager?.getEntries?.();
  if (!Array.isArray(entries)) return [];

  const messages: ChatMessage[] = [];
  for (const entry of entries) {
    if (!entry || typeof entry !== "object") continue;
    const record = entry as Record<string, unknown>;
    if (record.type !== "message") continue;
    const message =
      record.message && typeof record.message === "object"
        ? (record.message as Record<string, unknown>)
        : undefined;
    if (!message) continue;
    const role = normalizeRole(
      firstString(message, ["role", "sender", "author"]),
    );
    const text = extractText(message);
    if (role && text) {
      messages.push({ role, text });
    }
  }

  return messages.slice(-limit);
}

function recentChatMessagesFromFile(
  file: string,
  limit: number,
): ChatMessage[] {
  try {
    const raw = readFileSync(file, "utf8");
    const messages: ChatMessage[] = [];
    for (const value of parseSessionFile(raw)) {
      collectChatMessages(value, messages);
    }
    return messages.slice(-limit);
  } catch {
    return [];
  }
}

function parseSessionFile(raw: string): unknown[] {
  const trimmed = raw.trim();
  if (!trimmed) return [];
  try {
    return [JSON.parse(trimmed)];
  } catch {
    const values: unknown[] = [];
    for (const line of trimmed.split(/\r?\n/)) {
      const text = line.trim();
      if (!text) continue;
      try {
        values.push(JSON.parse(text));
      } catch {
        // Pi session formats can change; ignore lines that are not JSON records.
      }
    }
    return values;
  }
}

function collectChatMessages(value: unknown, out: ChatMessage[]): void {
  if (Array.isArray(value)) {
    for (const item of value) collectChatMessages(item, out);
    return;
  }
  if (!value || typeof value !== "object") return;

  const record = value as Record<string, unknown>;
  const role = normalizeRole(firstString(record, ["role", "sender", "author"]));
  const text = extractText(record);
  if (role && text) {
    out.push({ role, text });
    return;
  }

  for (const nested of Object.values(record)) {
    collectChatMessages(nested, out);
  }
}

function normalizeRole(role: string | undefined): string | undefined {
  const lower = role?.toLowerCase();
  if (lower === "user" || lower === "human") return "user";
  if (lower === "assistant" || lower === "agent" || lower === "ai") {
    return "assistant";
  }
  return undefined;
}

function extractText(record: Record<string, unknown>): string | undefined {
  for (const key of ["content", "text", "message"]) {
    const text = textFromValue(record[key]);
    if (text) return text;
  }
  return undefined;
}

function textFromValue(value: unknown): string | undefined {
  if (typeof value === "string") return value.trim() || undefined;
  if (Array.isArray(value)) {
    const parts = value.map(textFromValue).filter(Boolean) as string[];
    return parts.join("\n").trim() || undefined;
  }
  if (value && typeof value === "object") {
    const record = value as Record<string, unknown>;
    return textFromValue(record.text) || textFromValue(record.content);
  }
  return undefined;
}

function firstString(
  record: Record<string, unknown>,
  keys: string[],
): string | undefined {
  for (const key of keys) {
    if (typeof record[key] === "string") return record[key] as string;
  }
  return undefined;
}

async function submit(
  tool: string,
  toolCallID: string,
  input: ToolInput,
  threadID: string | undefined,
  chat: { context: string; count: number } | undefined,
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
      chat_context: chat?.context,
      chat_context_messages: chat?.count,
      context: chat?.context,
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
      const chat = chatContext(ctx);
      const submitted = await submit(
        event.toolName,
        event.toolCallId,
        input,
        sessionID(ctx),
        chat,
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
        reason: `atryum: tool call '${event.toolName}' was ${decided.status} by reviewer.${reviewerReason}`,
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
