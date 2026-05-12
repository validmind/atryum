CREATE TABLE IF NOT EXISTS approval_rules (
    id             TEXT    PRIMARY KEY,
    action         TEXT    NOT NULL,
    server_pattern TEXT    NOT NULL DEFAULT '*',
    tool_pattern   TEXT    NOT NULL DEFAULT '*',
    user_pattern   TEXT    NOT NULL DEFAULT '*',
    description    TEXT,
    enabled        INTEGER NOT NULL DEFAULT 1,
    rule_order     INTEGER NOT NULL DEFAULT 0,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_approval_rules_order ON approval_rules (rule_order ASC);
