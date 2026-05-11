-- Migration 002: Add server status and auth columns

ALTER TABLE mcp_servers ADD COLUMN auth_type TEXT NOT NULL DEFAULT 'none';
ALTER TABLE mcp_servers ADD COLUMN connection_status TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE mcp_servers ADD COLUMN auth_status TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE mcp_servers ADD COLUMN reauth_needed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mcp_servers ADD COLUMN last_checked_at TIMESTAMP;
ALTER TABLE mcp_servers ADD COLUMN last_check_ok INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mcp_servers ADD COLUMN last_error_summary TEXT;
ALTER TABLE mcp_servers ADD COLUMN action_required TEXT;
