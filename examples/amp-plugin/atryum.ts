// @i-know-the-amp-plugin-api-is-wip-and-very-experimental-right-now
//
// Atryum amp plugin
// -----------------
// Routes every amp tool.call through Atryum for human approval, then reports
// the execution outcome on tool.result. Atryum itself does not execute the
// tool — amp does. Atryum is the approval mediator and audit log.
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
//   ATRYUM_CHAT_MESSAGES_LIMIT
//                    recent Amp thread messages sent as LLM-as-judge context,
//                    default 100. Set to 0 to disable.
//   ATRYUM_AMP_THREADS_DIR
//                    override Amp thread JSON directory, default
//                    ~/.local/share/amp/threads.

import type { PluginAPI } from "@ampcode/plugin";
import {
  existsSync,
  readFileSync,
  readdirSync,
  statSync,
} from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

const API = process.env.ATRYUM_URL || "http://localhost:8080";
const SOURCE = process.env.ATRYUM_SOURCE || "amp";
const POLL_INTERVAL = Number(process.env.ATRYUM_POLL_MS || 2000);
const CHAT_MESSAGES_LIMIT = Number(process.env.ATRYUM_CHAT_MESSAGES_LIMIT || 100);
const MAX_MESSAGE_CHARS = 2000;
const AMP_THREADS_DIR =
  process.env.ATRYUM_AMP_THREADS_DIR ||
  join(homedir(), ".local", "share", "amp", "threads");
// Amp exposes the current thread ID as $THREAD_ID in the plugin process env.
const THREAD_ID = process.env.THREAD_ID || "";
// Harness identity for the Atryum invocations UI Agent column. Falls back
// to SOURCE for the name. ATRYUM_CLIENT_VERSION / AMP_VERSION are checked
// in case a deployment plumbs the build version through the env; safe to
// leave empty.
const CLIENT_NAME = process.env.ATRYUM_CLIENT_NAME || SOURCE;
const CLIENT_VERSION =
  process.env.ATRYUM_CLIENT_VERSION || process.env.AMP_VERSION || "";
// Self-declared agent identity. Atryum resolves the Agent Record via the
// agents.agent_ids JSON array, so any string the user has added to an
// Agent Record (e.g. "amp-local", "amp-alice", a service account id, etc.)
// will work. Not authenticated — for verified identity use OAuth.
const AGENT_ID = process.env.ATRYUM_AGENT_ID || "";

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

type ChatMessage = {
  role: string;
  text: string;
};

// toolUseID -> atryum invocation id, so tool.result can patch the right row.
const invocationMap = new Map<string, string>();

function normalizeRole(role: unknown): string | undefined {
  if (role !== "user" && role !== "assistant" && role !== "system") {
    return undefined;
  }
  return role;
}

function trimMessage(text: string): string {
  const compact = text.replace(/\s+\n/g, "\n").replace(/\n{3,}/g, "\n\n").trim();
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

function ampThreadFile(): string | undefined {
  if (!existsSync(AMP_THREADS_DIR)) return undefined;

  if (THREAD_ID) {
    const file = join(AMP_THREADS_DIR, `${THREAD_ID}.json`);
    if (existsSync(file)) return file;
  }

  try {
    return readdirSync(AMP_THREADS_DIR)
      .filter((name) => name.endsWith(".json"))
      .map((name) => {
        const file = join(AMP_THREADS_DIR, name);
        return { file, mtime: statSync(file).mtimeMs };
      })
      .sort((a, b) => b.mtime - a.mtime)[0]?.file;
  } catch {
    return undefined;
  }
}

function chatMessagesFromThreadFile(): ChatMessage[] {
  const file = ampThreadFile();
  if (!file) return [];
  try {
    return chatMessagesFromValue(JSON.parse(readFileSync(file, "utf8")));
  } catch {
    return [];
  }
}

function recentChatContext(ctx: unknown): { context: string; count: number } | undefined {
  if (!Number.isFinite(CHAT_MESSAGES_LIMIT) || CHAT_MESSAGES_LIMIT <= 0) {
    return undefined;
  }

  const contextMessages = chatMessagesFromContext(ctx);
  const messages =
    contextMessages.length > 0 ? contextMessages : chatMessagesFromThreadFile();
  const recent = messages.slice(-CHAT_MESSAGES_LIMIT);
  if (recent.length === 0) return undefined;

  return {
    count: recent.length,
    context: [
      `Recent Amp chat messages (oldest to newest, up to ${CHAT_MESSAGES_LIMIT}):`,
      ...recent.map((msg) => `- ${msg.role}: ${msg.text}`),
    ].join("\n"),
  };
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

async function submit(
  tool: string,
  toolUseID: string,
  input: Record<string, unknown>,
  chat: { context: string; count: number } | undefined
): Promise<InvocationResponse> {
  const res = await fetch(`${API}/api/v1/external/invocations`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      source: SOURCE,
      tool,
      description: describe(input),
      input,
      request_id: toolUseID,
      thread_id: THREAD_ID || undefined,
      chat_context: chat?.context,
      chat_context_messages: chat?.count,
      context: chat?.context,
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
    const res = await fetch(
      `${API}/api/v1/external/invocations/${invocationID}`
    );
    if (!res.ok) {
      throw new Error(`atryum poll failed: ${res.status}`);
    }
    const inv = (await res.json()) as InvocationResponse;
    if (inv.status !== "pending_approval" && inv.status !== "received") {
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
  }
): Promise<void> {
  await fetch(`${API}/api/v1/external/invocations/${invocationID}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

export default function (amp: PluginAPI) {
  amp.on("tool.call", async (event, ctx) => {
    try {
      const chat = recentChatContext(ctx);
      const submitted = await submit(
        event.tool,
        event.toolUseID,
        event.input,
        chat
      );
      invocationMap.set(event.toolUseID, submitted.invocation_id);

      // If rules already decided (auto_approve / auto_deny), skip polling.
      let decided = submitted;
      if (
        submitted.status === "pending_approval" ||
        submitted.status === "received"
      ) {
        ctx.logger.log(
          `atryum: submitted ${event.tool} as ${submitted.invocation_id} — awaiting approval`
        );
        decided = await poll(submitted.invocation_id);
      }

      if (decided.status === "approved") {
        await patchExecution(submitted.invocation_id, {
          execution_status: "running",
        });
        ctx.logger.log(
          `atryum: approved ${event.tool} (${submitted.invocation_id})`
        );
        return { action: "allow" };
      }
      ctx.logger.log(
        `atryum: rejected ${event.tool} (${submitted.invocation_id}, status=${decided.status})`
      );
      invocationMap.delete(event.toolUseID);
      return {
        action: "reject-and-continue",
        message: `atryum: tool call '${event.tool}' was ${decided.status} by reviewer.`,
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
