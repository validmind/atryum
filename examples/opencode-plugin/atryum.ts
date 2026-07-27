import { exec } from "node:child_process"
import { createHash } from "node:crypto"
import { readFileSync } from "node:fs"
import { mkdir, readFile, rename, writeFile } from "node:fs/promises"
import { homedir } from "node:os"
import { dirname, join } from "node:path"
import { promisify } from "node:util"
import type { Plugin } from "@opencode-ai/plugin"

const execAsync = promisify(exec)

const GLOBAL_CONFIG_FILE = join(homedir(), ".config", "opencode", "atryum.json")
let PROJECT_CONFIG_FILE = ""

let API = "http://localhost:8080"
let SOURCE = "opencode"
let POLL_INTERVAL = 2000
let CLIENT_NAME = SOURCE
let CLIENT_VERSION = process.env.OPENCODE_VERSION || ""
// Self-declared agent identity. Atryum resolves the Agent Record via the
// agents.agent_ids array. Not authenticated; for verified identity use OAuth.
let AGENT_ID = ""
let ACCESS_TOKEN = ""
let TOKEN_COMMAND = ""
let TOKEN_REFRESH_SKEW_MS = 60000
let TOKEN_COMMAND_TIMEOUT_MS = 10000
let STATE_DIR = join(homedir(), ".atryum", "opencode-plugin-state")
let TOKEN_CACHE_FILE = ""
let TOKEN_CACHE_KEY = ""
let cachedToken = ""
let cachedTokenExpiresAt = 0
let refreshPromise: Promise<string> | null = null

type InvocationStatus =
  | "received"
  | "executing"
  | "pending_approval"
  | "approved"
  | "denied"
  | "expired"
  | "cancelled"
  | "succeeded"
  | "failed"

type InvocationResponse = {
  invocation_id: string
  status: InvocationStatus
  error?: unknown
}

type ToolInput = Record<string, unknown>

type ToolHookInput = {
  tool: string
  sessionID: string
  callID: string
}

type InvocationState = ToolHookInput & {
  invocationID: string
}

const invocationMap = new Map<string, InvocationState>()

function envMs(name: string, fallback: number) {
  const n = Math.floor(Number(process.env[name] || fallback))
  return Number.isFinite(n) && n >= 0 ? n : fallback
}

function configString(value: unknown): string {
  return typeof value === "string" ? value.trim() : ""
}

function configMs(value: unknown, fallback: number): number {
  const raw =
    typeof value === "string" && value.trim()
      ? value
      : typeof value === "number"
        ? value
        : fallback
  const n = Math.floor(Number(raw))
  return Number.isFinite(n) && n >= 0 ? n : fallback
}

function envOrConfig(name: string, value: unknown, fallback = ""): string {
  return configString(process.env[name]) || configString(value) || fallback
}

function envOrConfigMs(name: string, value: unknown, fallback: number): number {
  if (process.env[name]) return envMs(name, fallback)
  return configMs(value, fallback)
}

function readConfigFile(path: string): ToolInput {
  try {
    return asRecord(JSON.parse(readFileSync(path, "utf8")))
  } catch {
    return {}
  }
}

function resetTokenState() {
  TOKEN_CACHE_FILE = TOKEN_COMMAND ? join(STATE_DIR, "token-cache.json") : ""
  // Ties the cached token to the command and server that produced it, so
  // switching ATRYUM_TOKEN_COMMAND or ATRYUM_URL invalidates the cache instead
  // of sending a token minted for a different identity or target. Trailing
  // slashes are stripped so equivalent URL spellings share one cache entry.
  TOKEN_CACHE_KEY = TOKEN_COMMAND
    ? createHash("sha256")
        .update(`${TOKEN_COMMAND}\n${API.replace(/\/+$/, "")}`)
        .digest("hex")
    : ""
  cachedToken = TOKEN_COMMAND ? "" : ACCESS_TOKEN
  cachedTokenExpiresAt =
    ACCESS_TOKEN && !TOKEN_COMMAND ? Number.POSITIVE_INFINITY : 0
  refreshPromise = null
}

function configureRuntime(directory: string, worktree?: string) {
  PROJECT_CONFIG_FILE = join(worktree || directory, ".opencode", "atryum.json")

  const globalConfig = readConfigFile(GLOBAL_CONFIG_FILE)
  const projectConfig = readConfigFile(PROJECT_CONFIG_FILE)
  const config = { ...globalConfig, ...projectConfig }

  API = envOrConfig("ATRYUM_URL", config.url, "http://localhost:8080")
  SOURCE = envOrConfig("ATRYUM_SOURCE", config.source, "opencode")
  POLL_INTERVAL = envOrConfigMs("ATRYUM_POLL_MS", config.pollMs, 2000)
  CLIENT_NAME = envOrConfig("ATRYUM_CLIENT_NAME", config.clientName, SOURCE)
  CLIENT_VERSION =
    envOrConfig("ATRYUM_CLIENT_VERSION", config.clientVersion) ||
    process.env.OPENCODE_VERSION ||
    ""
  AGENT_ID = envOrConfig("ATRYUM_AGENT_ID", config.agentId)
  ACCESS_TOKEN = envOrConfig("ATRYUM_ACCESS_TOKEN", config.accessToken)
  TOKEN_COMMAND = envOrConfig("ATRYUM_TOKEN_COMMAND", config.tokenCommand)
  TOKEN_REFRESH_SKEW_MS = envOrConfigMs(
    "ATRYUM_TOKEN_REFRESH_SKEW_MS",
    config.tokenRefreshSkewMs,
    60000,
  )
  TOKEN_COMMAND_TIMEOUT_MS = envOrConfigMs(
    "ATRYUM_TOKEN_COMMAND_TIMEOUT_MS",
    config.tokenCommandTimeoutMs,
    10000,
  )
  STATE_DIR = envOrConfig(
    "ATRYUM_STATE_DIR",
    config.stateDir,
    join(homedir(), ".atryum", "opencode-plugin-state"),
  )
  resetTokenState()
}

function authSetupHint(): string {
  const places = [PROJECT_CONFIG_FILE, GLOBAL_CONFIG_FILE].filter(Boolean)
  return `Configure Atryum auth with /atryum (if installed), ${places.join(
    " or ",
  )}, ATRYUM_ACCESS_TOKEN, or ATRYUM_TOKEN_COMMAND.`
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

function mapKey(input: ToolHookInput): string {
  return `${input.sessionID}:${input.callID}`
}

function asRecord(value: unknown): ToolInput {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as ToolInput
  }
  return {}
}

function jsonSafe(value: unknown): unknown {
  try {
    return JSON.parse(
      JSON.stringify(value, (_key, item) =>
        typeof item === "bigint" ? item.toString() : item,
      ),
    )
  } catch {
    return String(value)
  }
}

function describe(input: ToolInput): string {
  const parts = Object.entries(input || {})
    .filter(([, value]) => typeof value === "string")
    .map(([key, value]) => {
      const text = String(value)
      return `${key}: ${text.length > 200 ? `${text.slice(0, 200)}...` : text}`
    })
  return parts.join(" | ") || "(no string params)"
}

function looksFailed(output: unknown): boolean {
  const result = asRecord(output)
  const metadata = asRecord(result.metadata)

  if (result.isError === true || result.success === false) return true
  if (result.error != null) return true
  if (metadata.isError === true || metadata.success === false) return true
  if (metadata.error != null) return true

  const exitCode = metadata.exitCode ?? metadata.exit_code ?? result.exitCode
  if (typeof exitCode === "number" && exitCode !== 0) return true

  const status = metadata.status ?? result.status
  if (typeof status === "string") {
    return ["error", "failed", "failure"].includes(status.toLowerCase())
  }

  return false
}

function parseTokenResponse(raw: string): {
  accessToken: string
  expiresAt: number
} {
  const text = raw.trim()
  if (!text) throw new Error("token command returned no token")
  if (!text.startsWith("{")) {
    if (/\s/.test(text)) {
      throw new Error("raw token command output must not contain whitespace")
    }
    return { accessToken: text, expiresAt: Date.now() + 55 * 60 * 1000 }
  }

  const parsed = JSON.parse(text) as Record<string, unknown>
  const accessToken =
    typeof parsed.access_token === "string"
      ? parsed.access_token
      : typeof parsed.accessToken === "string"
        ? parsed.accessToken
        : typeof parsed.token === "string"
          ? parsed.token
          : ""
  if (!accessToken) {
    throw new Error("token command response did not include access_token")
  }
  if (/\s/.test(accessToken)) {
    throw new Error("token command response token must not contain whitespace")
  }

  const toMs = (s: number) => (s > 1e11 ? s : s * 1000)
  const expiry = (value: unknown) => {
    const n =
      typeof value === "string" && value.trim() ? Number(value) : Number(value)
    return Number.isFinite(n) && n > 0 ? n : 0
  }
  const expiresAtValue = expiry(parsed.expires_at) || expiry(parsed.expiresAt)
  const expiresIn = expiry(parsed.expires_in)
  const expiresAt = expiresAtValue
    ? toMs(expiresAtValue)
    : expiresIn
      ? Date.now() + expiresIn * 1000
      : Date.now() + 55 * 60 * 1000
  return { accessToken, expiresAt }
}

async function readTokenCache(): Promise<{
  token: string
  expiresAt: number
} | null> {
  if (!TOKEN_CACHE_FILE) return null
  try {
    const raw = await readFile(TOKEN_CACHE_FILE, "utf8")
    const { token, expiresAt, key } = JSON.parse(raw) as {
      token?: unknown
      expiresAt?: unknown
      key?: unknown
    }
    if (
      typeof token === "string" &&
      token &&
      typeof expiresAt === "number" &&
      key === TOKEN_CACHE_KEY &&
      Date.now() < expiresAt - TOKEN_REFRESH_SKEW_MS
    ) {
      return { token, expiresAt }
    }
  } catch {
    // cache miss or unreadable
  }
  return null
}

async function writeTokenCache(token: string, expiresAt: number) {
  if (!TOKEN_CACHE_FILE) return
  try {
    await mkdir(dirname(TOKEN_CACHE_FILE), { recursive: true })
    const tmp = `${TOKEN_CACHE_FILE}.${process.pid}.tmp`
    await writeFile(
      tmp,
      JSON.stringify({ token, expiresAt, key: TOKEN_CACHE_KEY }),
      { encoding: "utf8", mode: 0o600 },
    )
    await rename(tmp, TOKEN_CACHE_FILE)
  } catch {
    // ignore — in-memory cache still works
  }
}

async function refreshAccessToken(useFileCache: boolean): Promise<string> {
  if (useFileCache) {
    const fileCache = await readTokenCache()
    if (fileCache) {
      cachedToken = fileCache.token
      cachedTokenExpiresAt = fileCache.expiresAt
      return cachedToken
    }
  }

  const { stdout } = await execAsync(TOKEN_COMMAND, {
    timeout: TOKEN_COMMAND_TIMEOUT_MS,
    maxBuffer: 1024 * 1024,
  })
  const token = parseTokenResponse(stdout)
  cachedToken = token.accessToken
  cachedTokenExpiresAt = token.expiresAt
  await writeTokenCache(cachedToken, cachedTokenExpiresAt)
  return cachedToken
}

async function accessToken(forceRefresh = false): Promise<string> {
  if (!TOKEN_COMMAND) return ACCESS_TOKEN
  if (
    !forceRefresh &&
    cachedToken &&
    Date.now() < cachedTokenExpiresAt - TOKEN_REFRESH_SKEW_MS
  ) {
    return cachedToken
  }
  if (!forceRefresh && refreshPromise) return refreshPromise
  const p = refreshAccessToken(!forceRefresh).finally(() => {
    if (refreshPromise === p) refreshPromise = null
  })
  if (!forceRefresh) refreshPromise = p
  return p
}

async function atryumHeaders(
  contentType = false,
  forceRefresh = false,
): Promise<Record<string, string>> {
  const headers: Record<string, string> = {}
  if (contentType) headers["Content-Type"] = "application/json"
  const token = await accessToken(forceRefresh)
  if (token) headers.Authorization = `Bearer ${token}`
  return headers
}

async function atryumFetch(
  url: string,
  options: RequestInit & { contentType?: boolean } = {},
): Promise<Response> {
  const { contentType = false, ...init } = options
  init.headers = {
    ...(await atryumHeaders(contentType)),
    ...((options.headers as Record<string, string> | undefined) || {}),
  }

  let res = await fetch(url, init)
  if (res.status === 401 && TOKEN_COMMAND) {
    init.headers = {
      ...(await atryumHeaders(contentType, true)),
      ...((options.headers as Record<string, string> | undefined) || {}),
    }
    res = await fetch(url, init)
  }
  return res
}

async function responseError(action: string, res: Response): Promise<string> {
  if (res.status === 401) {
    return `${action}: 401 Unauthorized. ${authSetupHint()}`
  }
  return `${action}: ${res.status} ${await res.text()}`
}

async function submit(
  tool: string,
  callID: string,
  input: ToolInput,
  sessionID: string,
): Promise<InvocationResponse> {
  const body = {
    source: SOURCE,
    tool,
    description: describe(input),
    input: jsonSafe(input),
    request_id: callID,
    thread_id: sessionID || undefined,
    agent_id: AGENT_ID || undefined,
    client_name: CLIENT_NAME,
    client_version: CLIENT_VERSION || undefined,
  }

  const res = await atryumFetch(`${API}/api/v1/external/invocations`, {
    method: "POST",
    contentType: true,
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    throw new Error(await responseError("atryum submit failed", res))
  }
  return (await res.json()) as InvocationResponse
}

async function poll(invocationID: string): Promise<InvocationResponse> {
  while (true) {
    const res = await atryumFetch(
      `${API}/api/v1/external/invocations/${invocationID}`,
    )
    if (!res.ok) {
      throw new Error(await responseError("atryum poll failed", res))
    }
    const inv = (await res.json()) as InvocationResponse
    if (
      inv.status !== "pending_approval" &&
      inv.status !== "received" &&
      inv.status !== "executing"
    ) {
      return inv
    }
    await sleep(POLL_INTERVAL)
  }
}

async function patchExecution(
  invocationID: string,
  body: {
    execution_status: "running" | "completed" | "failed" | "cancelled"
    result?: unknown
    error?: unknown
    message?: string
  },
): Promise<void> {
  const res = await atryumFetch(`${API}/api/v1/external/invocations/${invocationID}`, {
    method: "PATCH",
    contentType: true,
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    throw new Error(await responseError("atryum patch failed", res))
  }
}

async function log(
  client: Parameters<Plugin>[0]["client"],
  level: "debug" | "info" | "warn" | "error",
  message: string,
  extra?: Record<string, unknown>,
): Promise<void> {
  try {
    await client.app.log({
      body: {
        service: "atryum-opencode-plugin",
        level,
        message,
        extra,
      },
    })
  } catch {
    // Logging must not affect tool execution policy.
  }
}

export default (async ({ client, directory, worktree }) => {
  configureRuntime(directory, worktree)

  return {
    "tool.execute.before": async (input, output) => {
      const args = asRecord(output.args)
      const key = mapKey(input)
      let submitted: InvocationResponse

      try {
        submitted = await submit(input.tool, input.callID, args, input.sessionID)
      } catch (err) {
        await log(client, "error", "failed to submit tool call", {
          tool: input.tool,
          error: String(err),
        })
        throw new Error(`atryum: failed to gate tool call: ${err}`)
      }

      invocationMap.set(key, {
        invocationID: submitted.invocation_id,
        tool: input.tool,
        sessionID: input.sessionID,
        callID: input.callID,
      })

      let decided = submitted
      if (
        submitted.status === "pending_approval" ||
        submitted.status === "received" ||
        submitted.status === "executing"
      ) {
        await log(client, "info", "awaiting approval", {
          tool: input.tool,
          invocation_id: submitted.invocation_id,
        })
        try {
          decided = await poll(submitted.invocation_id)
        } catch (err) {
          invocationMap.delete(key)
          await log(client, "error", "failed to poll approval", {
            tool: input.tool,
            invocation_id: submitted.invocation_id,
            error: String(err),
          })
          throw new Error(`atryum: failed to gate tool call: ${err}`)
        }
      }

      if (decided.status !== "approved") {
        invocationMap.delete(key)
        await log(client, "warn", "blocked tool call", {
          tool: input.tool,
          invocation_id: submitted.invocation_id,
          status: decided.status,
        })
        throw new Error(
          `atryum: tool call '${input.tool}' was ${decided.status} by reviewer.`,
        )
      }

      try {
        await patchExecution(submitted.invocation_id, {
          execution_status: "running",
        })
      } catch (err) {
        invocationMap.delete(key)
        await log(client, "error", "failed to mark invocation running", {
          tool: input.tool,
          invocation_id: submitted.invocation_id,
          error: String(err),
        })
        throw new Error(`atryum: failed to mark approved tool call running: ${err}`)
      }

      await log(client, "info", "approved tool call", {
        tool: input.tool,
        invocation_id: submitted.invocation_id,
      })
    },

    "tool.execute.after": async (input, output) => {
      const key = mapKey(input)
      const state = invocationMap.get(key)
      if (!state) return
      invocationMap.delete(key)

      try {
        if (looksFailed(output)) {
          await patchExecution(state.invocationID, {
            execution_status: "failed",
            error: jsonSafe(output),
          })
        } else {
          await patchExecution(state.invocationID, {
            execution_status: "completed",
            result: jsonSafe(output),
          })
        }
      } catch (err) {
        await log(client, "error", "failed to report tool result", {
          tool: input.tool,
          invocation_id: state.invocationID,
          error: String(err),
        })
      }
    },

    dispose: async () => {
      for (const state of invocationMap.values()) {
        try {
          await patchExecution(state.invocationID, {
            execution_status: "cancelled",
            message:
              "opencode shut down before the tool result was reported to Atryum.",
          })
        } catch {
          // Best-effort shutdown audit only.
        }
      }
      invocationMap.clear()
    },
  }
}) satisfies Plugin
