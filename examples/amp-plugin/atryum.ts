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
// to poison the judge). Instead, the plugin mints an Atryum session once at
// startup (POST /api/v1/external/sessions) and echoes the returned session_id
// on every submission. Atryum reconstructs the judge's context from the prior
// tool calls it recorded for that session, which it trusts at the appropriate
// level (tool outputs > tool inputs > nothing from agent chat).
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
//   ATRYUM_AMP_SESSION_FILE
//                    override Amp session JSON file, default
//                    ~/.local/share/amp/session.json. Used only to label the
//                    session with Amp's own thread id (client_session_id).

import type { PluginAPI } from "@ampcode/plugin";
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

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

type SessionResponse = {
  session_id: string;
};

// toolUseID -> atryum invocation id, so tool.result can patch the right row.
const invocationMap = new Map<string, string>();

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
        byTerminal as Record<string, { updatedAt?: unknown; lastThreadId?: unknown }>
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

// Atryum-minted session id, created lazily on the first tool call and reused
// for the lifetime of this plugin process. Atryum links every invocation
// carrying this id and reconstructs the judge's context from them.
let sessionPromise: Promise<string | undefined> | undefined;

async function createSession(): Promise<string | undefined> {
  const res = await fetch(`${API}/api/v1/external/sessions`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      harness: SOURCE,
      // Amp's own thread id, for cross-referencing only. Atryum keys off the
      // session_id it mints, not this.
      client_session_id: activeThreadID() || undefined,
      agent_id: AGENT_ID || undefined,
    }),
  });
  if (!res.ok) {
    throw new Error(`${res.status} ${await res.text()}`);
  }
  const body = (await res.json()) as SessionResponse;
  return body.session_id || undefined;
}

async function ensureSession(): Promise<string | undefined> {
  // Sessions are an optimization for richer judge context. If creation fails,
  // fall back to submitting without a session_id rather than blocking tools.
  if (!sessionPromise) {
    sessionPromise = createSession().catch(() => undefined);
  }
  return sessionPromise;
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
      request_id: toolUseID,
      thread_id: activeThreadID() || undefined,
      session_id: sessionID,
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
      const sessionID = await ensureSession();
      const submitted = await submit(
        event.tool,
        event.toolUseID,
        event.input,
        sessionID
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
