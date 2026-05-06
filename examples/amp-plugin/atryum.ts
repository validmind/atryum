// @i-know-the-amp-plugin-api-is-wip-and-very-experimental-right-now
//
// Atryum amp plugin
// -----------------
// Routes every amp tool.call through Atryum for human approval, then reports
// the execution outcome on tool.result. Atryum itself does not execute the
// tool — amp does. Atryum is the approval mediator and audit log.
//
// Configure via env:
//   ATRYUM_URL    base URL of the atryum server, default http://localhost:8080
//   ATRYUM_SOURCE label that shows up in the atryum UI as the "upstream"
//                 column, default "amp"
//   ATRYUM_POLL_MS poll interval in ms while waiting for approval, default 2000

import type { PluginAPI } from "@ampcode/plugin";

const API = process.env.ATRYUM_URL || "http://localhost:8080";
const SOURCE = process.env.ATRYUM_SOURCE || "amp";
const POLL_INTERVAL = Number(process.env.ATRYUM_POLL_MS || 2000);
// Amp exposes the current thread ID as $THREAD_ID in the plugin process env.
const THREAD_ID = process.env.THREAD_ID || "";

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

// toolUseID -> atryum invocation id, so tool.result can patch the right row.
const invocationMap = new Map<string, string>();

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
  input: Record<string, unknown>
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
      const submitted = await submit(event.tool, event.toolUseID, event.input);
      invocationMap.set(event.toolUseID, submitted.invocation_id);
      ctx.logger.log(
        `atryum: submitted ${event.tool} as ${submitted.invocation_id} — awaiting approval`
      );

      const decided = await poll(submitted.invocation_id);
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
