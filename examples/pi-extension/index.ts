import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const API = process.env.ATRYUM_URL || "http://localhost:8080";
const SOURCE = process.env.ATRYUM_SOURCE || "pi";
const POLL_INTERVAL = Number(process.env.ATRYUM_POLL_MS || 2000);
const CLIENT_NAME = process.env.ATRYUM_CLIENT_NAME || SOURCE;
const CLIENT_VERSION =
  process.env.ATRYUM_CLIENT_VERSION || process.env.PI_VERSION || "";
// Self-declared agent identity. Atryum resolves the Agent Record via the
// agents.agent_ids array. Not authenticated; for verified identity use OAuth.
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
  approval?: {
    reason?: string;
  };
  error?: unknown;
};

type SessionResponse = {
  session_id: string;
};

type ToolInput = Record<string, unknown>;

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

// Pi's own session identifier, used only for cross-referencing (thread_id and
// client_session_id). Atryum keys off the session_id it mints, not this.
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

// Atryum-minted session id, created lazily on the first tool call and reused
// for the lifetime of this extension. Atryum links every invocation carrying
// this id and reconstructs the judge's context from them — the extension never
// sends a free-form context blob.
let sessionPromise: Promise<string | undefined> | undefined;

async function createSession(
  clientSessionID: string | undefined
): Promise<string | undefined> {
  const res = await fetch(`${API}/api/v1/external/sessions`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      harness: SOURCE,
      client_session_id: clientSessionID || undefined,
      agent_id: AGENT_ID || undefined,
    }),
  });
  if (!res.ok) {
    throw new Error(`${res.status} ${await res.text()}`);
  }
  const body = (await res.json()) as SessionResponse;
  return body.session_id || undefined;
}

async function ensureSession(ctx: unknown): Promise<string | undefined> {
  // Sessions are an optimization for richer judge context. If creation fails,
  // fall back to submitting without a session_id rather than blocking tools.
  if (!sessionPromise) {
    sessionPromise = createSession(piClientSessionID(ctx)).catch(
      () => undefined
    );
  }
  return sessionPromise;
}

async function submit(
  tool: string,
  toolCallID: string,
  input: ToolInput,
  threadID: string | undefined,
  sessionID: string | undefined
): Promise<InvocationResponse> {
  const res = await fetch(`${API}/api/v1/external/invocations`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      source: SOURCE,
      tool,
      description: describe(input),
      input,
      request_id: toolCallID,
      thread_id: threadID,
      session_id: sessionID,
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
    const res = await fetch(
      `${API}/api/v1/external/invocations/${invocationID}`
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
  }
): Promise<void> {
  const res = await fetch(`${API}/api/v1/external/invocations/${invocationID}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`atryum patch failed: ${res.status} ${await res.text()}`);
  }
}

export default function (pi: ExtensionAPI) {
  pi.on("tool_call", async (event, ctx) => {
    try {
      const input = (event.input || {}) as ToolInput;
      const sessionID = await ensureSession(ctx);
      const submitted = await submit(
        event.toolName,
        event.toolCallId,
        input,
        piClientSessionID(ctx),
        sessionID
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
