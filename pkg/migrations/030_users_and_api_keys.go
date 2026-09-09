package migrations

// migration030 introduces the open-core identity model:
//
//   - users: a principal cache of humans seen at the operator API. Rows are
//     created just-in-time on the first valid IdP login and keyed by
//     (issuer, subject) — never by email, which can change or be reassigned.
//     The IdP remains the source of truth for who may log in; Atryum only
//     needs a stable row to hang ownership and key revocation off. Tenancy
//     is assumed to be one organization per Atryum instance.
//   - agent_members: which users own/operate which agents. A join table
//     (not an owner column) so team agents can have several humans and a
//     departing key creator does not orphan the agent.
//   - api_keys: first-party credentials that act *as* an agent. Only a
//     SHA-256 of the token is stored; the plaintext is shown once at issue
//     time. created_by is nullable so keys can still be issued in no-auth
//     deployments (no IdP, hence no users). A key is valid when it is not
//     revoked, not expired, and its creator (if any) is not disabled.
func migration030() Definition {
	return Definition{
		Version: 30,
		Name:    "030_users_and_api_keys",
		Steps: []Step{
			RawDialect("create users table", `
				CREATE TABLE IF NOT EXISTS users (
					id            TEXT      PRIMARY KEY,
					issuer        TEXT      NOT NULL,
					subject       TEXT      NOT NULL,
					email         TEXT      NOT NULL DEFAULT '',
					name          TEXT      NOT NULL DEFAULT '',
					role          TEXT      NOT NULL DEFAULT 'member',
					created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
					last_login_at TIMESTAMP NULL,
					disabled_at   TIMESTAMP NULL,
					UNIQUE(issuer, subject)
				)
			`, `
				CREATE TABLE IF NOT EXISTS users (
					id            TEXT        PRIMARY KEY,
					issuer        TEXT        NOT NULL,
					subject       TEXT        NOT NULL,
					email         TEXT        NOT NULL DEFAULT '',
					name          TEXT        NOT NULL DEFAULT '',
					role          TEXT        NOT NULL DEFAULT 'member',
					created_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
					last_login_at TIMESTAMPTZ NULL,
					disabled_at   TIMESTAMPTZ NULL,
					UNIQUE(issuer, subject)
				)
			`),
			RawDialect("create agent_members table", `
				CREATE TABLE IF NOT EXISTS agent_members (
					agent_id   TEXT      NOT NULL,
					user_id    TEXT      NOT NULL,
					role       TEXT      NOT NULL DEFAULT 'owner',
					created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
					PRIMARY KEY (agent_id, user_id),
					FOREIGN KEY(agent_id) REFERENCES agents(id) ON DELETE CASCADE,
					FOREIGN KEY(user_id)  REFERENCES users(id)  ON DELETE CASCADE
				)
			`, `
				CREATE TABLE IF NOT EXISTS agent_members (
					agent_id   TEXT        NOT NULL,
					user_id    TEXT        NOT NULL,
					role       TEXT        NOT NULL DEFAULT 'owner',
					created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
					PRIMARY KEY (agent_id, user_id),
					FOREIGN KEY(agent_id) REFERENCES agents(id) ON DELETE CASCADE,
					FOREIGN KEY(user_id)  REFERENCES users(id)  ON DELETE CASCADE
				)
			`),
			Raw("create agent_members user index", `
				CREATE INDEX IF NOT EXISTS idx_agent_members_user_id ON agent_members (user_id)
			`),
			RawDialect("create api_keys table", `
				CREATE TABLE IF NOT EXISTS api_keys (
					id           TEXT      PRIMARY KEY,
					agent_id     TEXT      NOT NULL,
					created_by   TEXT      NULL,
					name         TEXT      NOT NULL DEFAULT '',
					key_hash     TEXT      NOT NULL UNIQUE,
					key_prefix   TEXT      NOT NULL,
					created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
					expires_at   TIMESTAMP NULL,
					last_used_at TIMESTAMP NULL,
					revoked_at   TIMESTAMP NULL,
					revoked_by   TEXT      NULL,
					FOREIGN KEY(agent_id)   REFERENCES agents(id) ON DELETE CASCADE,
					FOREIGN KEY(created_by) REFERENCES users(id)  ON DELETE SET NULL
				)
			`, `
				CREATE TABLE IF NOT EXISTS api_keys (
					id           TEXT        PRIMARY KEY,
					agent_id     TEXT        NOT NULL,
					created_by   TEXT        NULL,
					name         TEXT        NOT NULL DEFAULT '',
					key_hash     TEXT        NOT NULL UNIQUE,
					key_prefix   TEXT        NOT NULL,
					created_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
					expires_at   TIMESTAMPTZ NULL,
					last_used_at TIMESTAMPTZ NULL,
					revoked_at   TIMESTAMPTZ NULL,
					revoked_by   TEXT        NULL,
					FOREIGN KEY(agent_id)   REFERENCES agents(id) ON DELETE CASCADE,
					FOREIGN KEY(created_by) REFERENCES users(id)  ON DELETE SET NULL
				)
			`),
			Raw("create api_keys agent index", `
				CREATE INDEX IF NOT EXISTS idx_api_keys_agent_id ON api_keys (agent_id)
			`),
			Raw("create api_keys created_by index", `
				CREATE INDEX IF NOT EXISTS idx_api_keys_created_by ON api_keys (created_by)
			`),
		},
	}
}
