-- Migration 001: Initial schema

CREATE TABLE IF NOT EXISTS invocations (
    invocation_id TEXT PRIMARY KEY,
    request_id TEXT,
    idempotency_key TEXT,
    tool_name TEXT NOT NULL,
    upstream_name TEXT NOT NULL,
    status TEXT NOT NULL,
    approval_json TEXT,
    request_json TEXT NOT NULL,
    response_json TEXT,
    error_json TEXT,
    submitted_at TIMESTAMP NOT NULL,
    completed_at TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_invocations_idempotency_key
    ON invocations(idempotency_key) WHERE idempotency_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_invocations_submitted_at
    ON invocations(submitted_at DESC);

CREATE INDEX IF NOT EXISTS idx_invocations_upstream_tool_status
    ON invocations(upstream_name, tool_name, status);

CREATE TABLE IF NOT EXISTS invocation_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    invocation_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    FOREIGN KEY(invocation_id) REFERENCES invocations(invocation_id)
);

CREATE INDEX IF NOT EXISTS idx_invocation_events_lookup
    ON invocation_events(invocation_id, id);

CREATE TABLE IF NOT EXISTS mcp_servers (
    name TEXT PRIMARY KEY,
    mode TEXT NOT NULL,
    base_url TEXT,
    auth_token TEXT,
    timeout_seconds INTEGER NOT NULL DEFAULT 30,
    command TEXT,
    args_json TEXT NOT NULL DEFAULT '[]',
    env_json TEXT NOT NULL DEFAULT '{}',
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_mcp_servers_enabled_name
    ON mcp_servers(enabled, name);
