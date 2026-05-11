-- Migration 003: Add OAuth provider columns to mcp_servers, plus OAuth tables

ALTER TABLE mcp_servers ADD COLUMN oauth_provider_id TEXT;
ALTER TABLE mcp_servers ADD COLUMN oauth_provider_label TEXT;
ALTER TABLE mcp_servers ADD COLUMN oauth_authorize_url TEXT;
ALTER TABLE mcp_servers ADD COLUMN oauth_token_url TEXT;
ALTER TABLE mcp_servers ADD COLUMN oauth_client_id TEXT;
ALTER TABLE mcp_servers ADD COLUMN oauth_client_secret TEXT;
ALTER TABLE mcp_servers ADD COLUMN oauth_scopes TEXT;

CREATE TABLE IF NOT EXISTS oauth_credentials (
    server_name TEXT PRIMARY KEY,
    access_token TEXT NOT NULL,
    refresh_token TEXT,
    token_type TEXT,
    scope TEXT,
    expires_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    FOREIGN KEY(server_name) REFERENCES mcp_servers(name) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS oauth_connect_sessions (
    state TEXT PRIMARY KEY,
    server_name TEXT NOT NULL,
    status TEXT NOT NULL,
    code_verifier TEXT,
    redirect_uri TEXT NOT NULL,
    started_at TIMESTAMP NOT NULL,
    completed_at TIMESTAMP,
    error_message TEXT,
    FOREIGN KEY(server_name) REFERENCES mcp_servers(name) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_oauth_connect_sessions_server
    ON oauth_connect_sessions(server_name, started_at DESC);
