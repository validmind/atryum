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
// Some hosts include messages or a transcript_path/conversation_path in hook
// input. When available, this hook sends recent chat messages as
// LLM-as-judge context.

import { createHash } from "node:crypto";
import { mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

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
const CHAT_MESSAGES_LIMIT = Number(process.env.ATRYUM_CHAT_MESSAGES_LIMIT || 100);
const MAX_MESSAGE_CHARS = Number(process.env.ATRYUM_MAX_MESSAGE_CHARS || 2000);
// Self-declared agent identity sent to Atryum as the invocation `agent_id`.
// When this string is listed in an Agent Record's `agent_ids` array in the
// Atryum UI, invocations from this hook get tagged to that Agent Record
// (so agent-scoped approval rules apply). Not authenticated — for verified
// identity use OAuth. Default: empty (no agent tagging).
const AGENT_ID = process.env.ATRYUM_AGENT_ID || "";
const ACCESS_TOKEN = process.env.ATRYUM_ACCESS_TOKEN || "";

function atryumHeaders(contentType = false) {
  const headers = {};
  if (contentType) headers["Content-Type"] = "application/json";
  if (ACCESS_TOKEN) headers.Authorization = `Bearer ${ACCESS_TOKEN}`;
  return headers;
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
  return event.tool_name || event.toolName || event.tool || event.name || "unknown";
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
      ""
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

const RULES_CACHE_TTL_MS = 5 * 60 * 1000;
const rulesCache = new Map();

function formatRulesContext(rules) {
  if (!rules || typeof rules !== "object") return "";
  const lines = [
    "Atryum advisory rules visible to this harness before the gated call:",
    `- server: ${rules.server || SOURCE}`,
    `- tool: ${rules.tool || "unknown"}`,
    `- effective action: ${rules.action || rules.default_action || "unknown"}`,
  ];
  if (rules.matched_rule_id) {
    lines.push(`- matched rule: ${rules.matched_rule_id}`);
  }
  if (rules.generated_at) {
    lines.push(`- as of: ${rules.generated_at}`);
  }
  if (Array.isArray(rules.items) && rules.items.length > 0) {
    lines.push("- visible rules:");
    for (const rule of rules.items.slice(0, 20)) {
      const guidance = rule.guidance ? ` (${rule.guidance})` : "";
      lines.push(`  - ${rule.id || "(unnamed)"}: ${rule.action}${guidance}`);
    }
    if (rules.items.length > 20) {
      lines.push(`  - ...${rules.items.length - 20} more`);
    }
  }
  lines.push("- advisory only; Atryum re-checks policy during the actual gated call.");
  return lines.join("\n");
}

async function rulesContext(tool) {
  const cacheKey = [SOURCE, tool, ACCESS_TOKEN ? "auth" : "no-auth", AGENT_ID].join("\x00");
  const cached = rulesCache.get(cacheKey);
  if (cached !== undefined && cached.expiresAt > Date.now()) return cached.value;
  if (cached !== undefined) rulesCache.delete(cacheKey);
  const url = new URL("/api/v1/agent/rules", API);
  url.searchParams.set("server", SOURCE);
  url.searchParams.set("tool", tool);
  if (AGENT_ID && !ACCESS_TOKEN) {
    url.searchParams.set("agent_id", AGENT_ID);
  }
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 3000);
  try {
    const res = await fetch(url, { headers: atryumHeaders(), signal: controller.signal });
    if (!res.ok) return "";
    const result = formatRulesContext(await res.json());
    rulesCache.set(cacheKey, {
      value: result,
      expiresAt: Date.now() + RULES_CACHE_TTL_MS,
    });
    return result;
  } catch {
    return "";
  } finally {
    clearTimeout(timer);
  }
}

function combineContext(rules, chat) {
  const context = [rules, chat?.context].filter(Boolean).join("\n\n");
  if (!context) return undefined;
  return { context, count: chat?.count };
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
    const status = typeof record.status === "string" ? record.status : "completed";
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
    record.message && typeof record.message === "object" ? record.message : undefined;
  const role = normalizeRole(
    nested?.role ||
      record.role ||
      record.type ||
      record.sender ||
      record.author
  );
  if (!role) return undefined;

  const text = trimMessage(
    extractText(nested?.content ?? record.content ?? nested ?? record)
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

function codexHome() {
  return process.env.CODEX_HOME || path.join(os.homedir(), ".codex");
}

async function findCodexSessionPath(id) {
  if (!id) return "";
  const root = path.join(codexHome(), "sessions");
  const stack = [root];

  while (stack.length > 0) {
    const dir = stack.pop();
    let entries = [];
    try {
      entries = await readdir(dir, { withFileTypes: true });
    } catch {
      continue;
    }

    for (const entry of entries) {
      const fullPath = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        stack.push(fullPath);
      } else if (entry.isFile() && entry.name.endsWith(".jsonl") && entry.name.includes(id)) {
        return fullPath;
      }
    }
  }

  return "";
}

async function codexSessionMessages(event) {
  const id = sessionId(event) || (await latestCodexHistorySessionId());
  const sessionPath = await findCodexSessionPath(id);
  if (sessionPath) {
    try {
      return parseChatMessages(await readFile(sessionPath, "utf8"));
    } catch {
      return [];
    }
  }

  if (!id) return [];
  try {
    const raw = await readFile(path.join(codexHome(), "history.jsonl"), "utf8");
    return raw
      .split(/\r?\n/)
      .map((line) => {
        if (!line.trim()) return undefined;
        try {
          const record = JSON.parse(line);
          if (record.session_id !== id || !record.text) return undefined;
          return { role: "user", text: trimMessage(record.text) };
        } catch {
          return undefined;
        }
      })
      .filter(Boolean);
  } catch {
    return [];
  }
}

async function latestCodexHistorySessionId() {
  try {
    const raw = await readFile(path.join(codexHome(), "history.jsonl"), "utf8");
    const lines = raw.trim().split(/\r?\n/);
    for (let i = lines.length - 1; i >= 0; i -= 1) {
      try {
        const record = JSON.parse(lines[i]);
        if (record.session_id) return record.session_id;
      } catch {
        // Keep scanning older history lines.
      }
    }
  } catch {
    return "";
  }
  return "";
}

async function chatContext(event) {
  if (CHAT_MESSAGES_LIMIT <= 0) return undefined;

  let messages = chatMessagesFromEvent(event);

  if (messages.length === 0 && HOST === "codex") {
    messages = await codexSessionMessages(event);
  }

  if (messages.length === 0) {
    const file = chatHistoryPath(event);
    if (!file) return undefined;

    let raw = "";
    try {
      raw = await readFile(file, "utf8");
    } catch {
      return undefined;
    }
    messages = parseChatMessages(raw);
  }

  const recent = messages.slice(-CHAT_MESSAGES_LIMIT);
  if (recent.length === 0) return undefined;

  const hostLabel =
    HOST === "claude"
      ? "Claude Code"
      : HOST === "cursor"
        ? "Cursor"
        : HOST === "codex"
          ? "Codex"
          : SOURCE;

  return {
    count: recent.length,
    context: [
      `Recent ${hostLabel} chat messages (oldest to newest, up to ${CHAT_MESSAGES_LIMIT}):`,
      ...recent.map((msg) => `- ${msg.role}: ${msg.text}`),
    ].join("\n"),
  };
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
    "utf8"
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

async function submit(event) {
  const name = toolName(event);
  const input = toolInput(event);
  const id = toolUseID(event);
  const chat = await chatContext(event);
  const context = combineContext(await rulesContext(name), chat);
  const res = await fetch(`${API}/api/v1/external/invocations`, {
    method: "POST",
    headers: atryumHeaders(true),
    body: JSON.stringify({
      source: SOURCE,
      tool: name,
      description: describe(input),
      input,
      request_id: id,
      thread_id: sessionId(event) || undefined,
      chat_context: context?.context,
      chat_context_messages: context?.count,
      context: context?.context,
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
    const res = await fetch(`${API}/api/v1/external/invocations/${invocationId}`, {
      headers: atryumHeaders(),
    });
    if (!res.ok) {
      throw new Error(`atryum poll failed: ${res.status}`);
    }
    const inv = await res.json();
    if (inv.status !== "pending_approval" && inv.status !== "received" && inv.status !== "executing") {
      return inv;
    }
    await sleep(POLL_INTERVAL);
  }
}

async function patchExecution(invocationId, body) {
  const res = await fetch(`${API}/api/v1/external/invocations/${invocationId}`, {
    method: "PATCH",
    headers: atryumHeaders(true),
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`atryum patch failed: ${res.status} ${await res.text()}`);
  }
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
  return event.tool_response || event.toolResponse || event.output || event.result;
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
    submitted.status === "received"
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
      `atryum: tool call '${toolName(event)}' was ${decided.status} by reviewer.`
    )
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
  jsonOut(denyOutput(`atryum: failed to gate tool call: ${err.message || err}`));
  process.exitCode = 0;
});
