#!/usr/bin/env node
// Atryum command hook for agents that expose PreToolUse/PostToolUse style hooks.
//
// Supported host modes:
//   ATRYUM_HOOK_HOST=claude  Claude Code settings hooks
//   ATRYUM_HOOK_HOST=cursor  Cursor hooks
//
// The hook submits PreToolUse events to Atryum's external invocation API, blocks
// until the invocation is approved or denied, and reports successful PostToolUse
// results back to the same invocation.

import { createHash } from "node:crypto";
import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

const API = process.env.ATRYUM_URL || "http://localhost:8080";
const SOURCE = process.env.ATRYUM_SOURCE || process.env.ATRYUM_HOOK_HOST || "agent";
const POLL_INTERVAL = Number(process.env.ATRYUM_POLL_MS || 2000);
const HOST = (process.env.ATRYUM_HOOK_HOST || "claude").toLowerCase();
const STATE_DIR =
  process.env.ATRYUM_STATE_DIR ||
  path.join(os.homedir(), ".atryum", "agent-hook-state");

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
  const res = await fetch(`${API}/api/v1/external/invocations`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      source: SOURCE,
      tool: name,
      description: describe(input),
      input,
      request_id: id,
      thread_id: event.session_id || event.sessionId || undefined,
    }),
  });
  if (!res.ok) {
    throw new Error(`atryum submit failed: ${res.status} ${await res.text()}`);
  }
  return res.json();
}

async function poll(invocationId) {
  while (true) {
    const res = await fetch(`${API}/api/v1/external/invocations/${invocationId}`);
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
    headers: { "Content-Type": "application/json" },
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
  if (status === "error") {
    await patchExecution(invocationId, {
      execution_status: "failed",
      error: toolResult(event),
    });
  } else {
    await patchExecution(invocationId, {
      execution_status: "completed",
      result: toolResult(event),
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
