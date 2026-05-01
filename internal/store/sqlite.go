package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"

	"atryum/internal/invocation"
	"atryum/internal/mcp"
)

var psql = sq.StatementBuilder.PlaceholderFormat(sq.Question)

type InvocationRepo struct{ db *sql.DB }
type EventRepo struct{ db *sql.DB }
type ServerRepo struct{ db *sql.DB }

func NewInvocationRepo(db *sql.DB) *InvocationRepo { return &InvocationRepo{db: db} }
func NewEventRepo(db *sql.DB) *EventRepo           { return &EventRepo{db: db} }
func NewServerRepo(db *sql.DB) *ServerRepo         { return &ServerRepo{db: db} }

func InitDB(db *sql.DB) error {
	if _, err := db.Exec(`PRAGMA foreign_keys = ON;`); err != nil {
		return err
	}
	schema := `
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
	CREATE UNIQUE INDEX IF NOT EXISTS idx_invocations_idempotency_key ON invocations(idempotency_key) WHERE idempotency_key IS NOT NULL;
	CREATE INDEX IF NOT EXISTS idx_invocations_submitted_at ON invocations(submitted_at DESC);
	CREATE INDEX IF NOT EXISTS idx_invocations_upstream_tool_status ON invocations(upstream_name, tool_name, status);
	CREATE TABLE IF NOT EXISTS invocation_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		invocation_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		FOREIGN KEY(invocation_id) REFERENCES invocations(invocation_id)
	);
	CREATE INDEX IF NOT EXISTS idx_invocation_events_lookup ON invocation_events(invocation_id, id);
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
		auth_type TEXT NOT NULL DEFAULT 'none',
		connection_status TEXT NOT NULL DEFAULT 'unknown',
		auth_status TEXT NOT NULL DEFAULT 'unknown',
		reauth_needed INTEGER NOT NULL DEFAULT 0,
		last_checked_at TIMESTAMP,
		last_check_ok INTEGER NOT NULL DEFAULT 0,
		last_error_summary TEXT,
		action_required TEXT,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_mcp_servers_enabled_name ON mcp_servers(enabled, name);
	ALTER TABLE mcp_servers ADD COLUMN auth_type TEXT NOT NULL DEFAULT 'none';
	ALTER TABLE mcp_servers ADD COLUMN connection_status TEXT NOT NULL DEFAULT 'unknown';
	ALTER TABLE mcp_servers ADD COLUMN auth_status TEXT NOT NULL DEFAULT 'unknown';
	ALTER TABLE mcp_servers ADD COLUMN reauth_needed INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE mcp_servers ADD COLUMN last_checked_at TIMESTAMP;
	ALTER TABLE mcp_servers ADD COLUMN last_check_ok INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE mcp_servers ADD COLUMN last_error_summary TEXT;
	ALTER TABLE mcp_servers ADD COLUMN action_required TEXT;
	`
	_, err := db.Exec(schema)
	if err != nil {
		// SQLite may fail on duplicate ALTERs; apply best-effort compatibility migration.
		if fixErr := ensureServerStatusColumns(db); fixErr != nil {
			return fixErr
		}
	}
	return nil
}

func (r *InvocationRepo) Create(ctx context.Context, inv invocation.Invocation) error {
	var approval any
	if inv.Approval != nil {
		b, _ := json.Marshal(inv.Approval)
		approval = string(b)
	}
	query, args, err := psql.Insert("invocations").Columns(
		"invocation_id", "request_id", "idempotency_key", "tool_name", "upstream_name", "status", "approval_json", "request_json", "response_json", "error_json", "submitted_at", "completed_at",
	).Values(
		inv.InvocationID, inv.RequestID, inv.IdempotencyKey, inv.Tool, inv.Upstream, inv.Status, approval, string(inv.Input), nullableString(inv.Response), nullableString(inv.Error), inv.SubmittedAt, inv.CompletedAt,
	).ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *InvocationRepo) UpdateResult(ctx context.Context, inv invocation.Invocation) error {
	var approval any
	if inv.Approval != nil {
		b, _ := json.Marshal(inv.Approval)
		approval = string(b)
	}
	query, args, err := psql.Update("invocations").Set("status", inv.Status).Set("approval_json", approval).Set("response_json", nullableString(inv.Response)).Set("error_json", nullableString(inv.Error)).Set("completed_at", inv.CompletedAt).Where(sq.Eq{"invocation_id": inv.InvocationID}).ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *InvocationRepo) Get(ctx context.Context, id string) (invocation.Invocation, error) {
	query, args, err := psql.Select("invocation_id", "request_id", "idempotency_key", "tool_name", "upstream_name", "status", "approval_json", "request_json", "response_json", "error_json", "submitted_at", "completed_at").From("invocations").Where(sq.Eq{"invocation_id": id}).ToSql()
	if err != nil {
		return invocation.Invocation{}, err
	}
	row := r.db.QueryRowContext(ctx, query, args...)
	return scanInvocation(row)
}

func (r *InvocationRepo) GetByIdempotencyKey(ctx context.Context, key string) (invocation.Invocation, error) {
	query, args, err := psql.Select("invocation_id", "request_id", "idempotency_key", "tool_name", "upstream_name", "status", "approval_json", "request_json", "response_json", "error_json", "submitted_at", "completed_at").From("invocations").Where(sq.Eq{"idempotency_key": key}).ToSql()
	if err != nil {
		return invocation.Invocation{}, err
	}
	row := r.db.QueryRowContext(ctx, query, args...)
	return scanInvocation(row)
}

func (r *InvocationRepo) List(ctx context.Context, filter invocation.InvocationListFilter) ([]invocation.Invocation, int, error) {
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	builder := psql.Select("invocation_id", "request_id", "idempotency_key", "tool_name", "upstream_name", "status", "approval_json", "request_json", "response_json", "error_json", "submitted_at", "completed_at").From("invocations")
	countBuilder := psql.Select("COUNT(*)").From("invocations")
	builder, countBuilder = applyInvocationFilter(builder, countBuilder, filter)
	query, args, err := builder.OrderBy("submitted_at DESC").Limit(filter.Limit).Offset(filter.Offset).ToSql()
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []invocation.Invocation
	for rows.Next() {
		inv, err := scanInvocation(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	total, err := countRows(ctx, r.db, countBuilder)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (r *EventRepo) Create(ctx context.Context, evt invocation.Event) error {
	query, args, err := psql.Insert("invocation_events").Columns("invocation_id", "event_type", "payload_json", "created_at").Values(evt.InvocationID, evt.EventType, string(evt.Payload), evt.CreatedAt).ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *EventRepo) ListByInvocation(ctx context.Context, invocationID string, filter invocation.EventListFilter) ([]invocation.Event, int, error) {
	if filter.Limit == 0 {
		filter.Limit = 200
	}
	builder := psql.Select("id", "invocation_id", "event_type", "payload_json", "created_at").From("invocation_events").Where(sq.Eq{"invocation_id": invocationID})
	query, args, err := builder.OrderBy("id ASC").Limit(filter.Limit).Offset(filter.Offset).ToSql()
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []invocation.Event
	for rows.Next() {
		var evt invocation.Event
		var payload string
		if err := rows.Scan(&evt.ID, &evt.InvocationID, &evt.EventType, &payload, &evt.CreatedAt); err != nil {
			return nil, 0, err
		}
		evt.Payload = []byte(payload)
		out = append(out, evt)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	countBuilder := psql.Select("COUNT(*)").From("invocation_events").Where(sq.Eq{"invocation_id": invocationID})
	total, err := countRows(ctx, r.db, countBuilder)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (r *ServerRepo) CountServers(ctx context.Context) (int, error) {
	query, args, err := psql.Select("COUNT(*)").From("mcp_servers").ToSql()
	if err != nil {
		return 0, err
	}
	var total int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (r *ServerRepo) CreateServer(ctx context.Context, upstream mcp.Upstream) error {
	now := time.Now().UTC()
	argsJSON, envJSON, err := encodeServerConfig(upstream)
	if err != nil {
		return err
	}
	query, args, err := psql.Insert("mcp_servers").Columns(
		"name", "mode", "base_url", "auth_token", "timeout_seconds", "command", "args_json", "env_json", "enabled", "auth_type", "connection_status", "auth_status", "reauth_needed", "last_checked_at", "last_check_ok", "last_error_summary", "action_required", "created_at", "updated_at",
	).Values(
		upstream.Name, string(upstream.Mode), emptyToNil(upstream.BaseURL), emptyToNil(upstream.AuthToken), int(upstream.Timeout/time.Second), emptyToNil(upstream.Command), argsJSON, envJSON, boolToInt(upstream.Enabled), string(upstream.Status.AuthType), string(upstream.Status.ConnectionStatus), string(upstream.Status.AuthStatus), boolToInt(upstream.Status.ReauthNeeded), upstream.Status.LastCheckedAt, boolToInt(upstream.Status.LastCheckOK), emptyToNil(derefString(upstream.Status.LastErrorSummary)), emptyToNil(derefString(upstream.Status.ActionRequired)), now, now,
	).ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *ServerRepo) UpsertServer(ctx context.Context, upstream mcp.Upstream) error {
	argsJSON, envJSON, err := encodeServerConfig(upstream)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	query := `
	INSERT INTO mcp_servers (name, mode, base_url, auth_token, timeout_seconds, command, args_json, env_json, enabled, auth_type, connection_status, auth_status, reauth_needed, last_checked_at, last_check_ok, last_error_summary, action_required, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(name) DO UPDATE SET
		mode = excluded.mode,
		base_url = excluded.base_url,
		auth_token = excluded.auth_token,
		timeout_seconds = excluded.timeout_seconds,
		command = excluded.command,
		args_json = excluded.args_json,
		env_json = excluded.env_json,
		enabled = excluded.enabled,
		auth_type = excluded.auth_type,
		connection_status = excluded.connection_status,
		auth_status = excluded.auth_status,
		reauth_needed = excluded.reauth_needed,
		last_checked_at = excluded.last_checked_at,
		last_check_ok = excluded.last_check_ok,
		last_error_summary = excluded.last_error_summary,
		action_required = excluded.action_required,
		updated_at = excluded.updated_at
	`
	_, err = r.db.ExecContext(ctx, query,
		upstream.Name, string(upstream.Mode), emptyToNil(upstream.BaseURL), emptyToNil(upstream.AuthToken), int(upstream.Timeout/time.Second), emptyToNil(upstream.Command), argsJSON, envJSON, boolToInt(upstream.Enabled), string(upstream.Status.AuthType), string(upstream.Status.ConnectionStatus), string(upstream.Status.AuthStatus), boolToInt(upstream.Status.ReauthNeeded), upstream.Status.LastCheckedAt, boolToInt(upstream.Status.LastCheckOK), emptyToNil(derefString(upstream.Status.LastErrorSummary)), emptyToNil(derefString(upstream.Status.ActionRequired)), now, now,
	)
	return err
}

func (r *ServerRepo) UpdateServerStatus(ctx context.Context, name string, status mcp.ServerStatus) error {
	query, args, err := psql.Update("mcp_servers").
		Set("auth_type", string(status.AuthType)).
		Set("connection_status", string(status.ConnectionStatus)).
		Set("auth_status", string(status.AuthStatus)).
		Set("reauth_needed", boolToInt(status.ReauthNeeded)).
		Set("last_checked_at", status.LastCheckedAt).
		Set("last_check_ok", boolToInt(status.LastCheckOK)).
		Set("last_error_summary", emptyToNil(derefString(status.LastErrorSummary))).
		Set("action_required", emptyToNil(derefString(status.ActionRequired))).
		Set("updated_at", time.Now().UTC()).
		Where(sq.Eq{"name": name}).ToSql()
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return err
}

func (r *ServerRepo) GetServer(ctx context.Context, name string) (mcp.Upstream, error) {
	query, args, err := psql.Select("name", "mode", "base_url", "auth_token", "timeout_seconds", "command", "args_json", "env_json", "enabled", "auth_type", "connection_status", "auth_status", "reauth_needed", "last_checked_at", "last_check_ok", "last_error_summary", "action_required").From("mcp_servers").Where(sq.Eq{"name": name, "enabled": 1}).ToSql()
	if err != nil {
		return mcp.Upstream{}, err
	}
	row := r.db.QueryRowContext(ctx, query, args...)
	return scanServer(row)
}

func (r *ServerRepo) GetServerAny(ctx context.Context, name string) (mcp.Upstream, error) {
	query, args, err := psql.Select("name", "mode", "base_url", "auth_token", "timeout_seconds", "command", "args_json", "env_json", "enabled", "auth_type", "connection_status", "auth_status", "reauth_needed", "last_checked_at", "last_check_ok", "last_error_summary", "action_required").From("mcp_servers").Where(sq.Eq{"name": name}).ToSql()
	if err != nil {
		return mcp.Upstream{}, err
	}
	row := r.db.QueryRowContext(ctx, query, args...)
	return scanServer(row)
}

func (r *ServerRepo) ListServers(ctx context.Context, filter mcp.ServerFilter) ([]mcp.Upstream, int, error) {
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	builder := psql.Select("name", "mode", "base_url", "auth_token", "timeout_seconds", "command", "args_json", "env_json", "enabled", "auth_type", "connection_status", "auth_status", "reauth_needed", "last_checked_at", "last_check_ok", "last_error_summary", "action_required").From("mcp_servers")
	countBuilder := psql.Select("COUNT(*)").From("mcp_servers")
	if filter.Enabled != nil {
		value := boolToInt(*filter.Enabled)
		builder = builder.Where(sq.Eq{"enabled": value})
		countBuilder = countBuilder.Where(sq.Eq{"enabled": value})
	}
	query, args, err := builder.OrderBy("name ASC").Limit(filter.Limit).Offset(filter.Offset).ToSql()
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []mcp.Upstream
	for rows.Next() {
		upstream, err := scanServer(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, upstream)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	total, err := countRows(ctx, r.db, countBuilder)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (r *ServerRepo) DeleteServer(ctx context.Context, name string) error {
	query, args, err := psql.Delete("mcp_servers").Where(sq.Eq{"name": name}).ToSql()
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return err
}

func (r *ServerRepo) DisableServer(ctx context.Context, name string) error {
	query, args, err := psql.Update("mcp_servers").Set("enabled", 0).Set("connection_status", string(mcp.ConnectionStatusDisabled)).Set("updated_at", time.Now().UTC()).Where(sq.Eq{"name": name}).ToSql()
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return err
}

func scanInvocation(scanner interface{ Scan(dest ...any) error }) (invocation.Invocation, error) {
	var inv invocation.Invocation
	var approval sql.NullString
	var requestID, idempotencyKey sql.NullString
	var requestJSON, responseJSON, errorJSON sql.NullString
	var completedAt sql.NullTime
	if err := scanner.Scan(&inv.InvocationID, &requestID, &idempotencyKey, &inv.Tool, &inv.Upstream, &inv.Status, &approval, &requestJSON, &responseJSON, &errorJSON, &inv.SubmittedAt, &completedAt); err != nil {
		return invocation.Invocation{}, err
	}
	if requestID.Valid {
		inv.RequestID = &requestID.String
	}
	if idempotencyKey.Valid {
		inv.IdempotencyKey = &idempotencyKey.String
	}
	inv.Input = []byte(requestJSON.String)
	if responseJSON.Valid {
		inv.Response = []byte(responseJSON.String)
	}
	if errorJSON.Valid {
		inv.Error = []byte(errorJSON.String)
	}
	if completedAt.Valid {
		t := completedAt.Time
		inv.CompletedAt = &t
	}
	if approval.Valid {
		var ap invocation.Approval
		if err := json.Unmarshal([]byte(approval.String), &ap); err == nil {
			inv.Approval = &ap
		}
	}
	return inv, nil
}

func scanServer(scanner interface{ Scan(dest ...any) error }) (mcp.Upstream, error) {
	var upstream mcp.Upstream
	var mode string
	var baseURL, authToken, command sql.NullString
	var timeoutSeconds int
	var argsJSON, envJSON string
	var enabled int
	var authType, connectionStatus, authStatus sql.NullString
	var reauthNeeded, lastCheckOK int
	var lastCheckedAt sql.NullTime
	var lastErrorSummary, actionRequired sql.NullString
	if err := scanner.Scan(&upstream.Name, &mode, &baseURL, &authToken, &timeoutSeconds, &command, &argsJSON, &envJSON, &enabled, &authType, &connectionStatus, &authStatus, &reauthNeeded, &lastCheckedAt, &lastCheckOK, &lastErrorSummary, &actionRequired); err != nil {
		return mcp.Upstream{}, err
	}
	upstream.Mode = mcp.UpstreamMode(mode)
	upstream.BaseURL = baseURL.String
	upstream.AuthToken = authToken.String
	upstream.Timeout = time.Duration(timeoutSeconds) * time.Second
	upstream.Command = command.String
	upstream.Enabled = enabled == 1
	if err := json.Unmarshal([]byte(argsJSON), &upstream.Args); err != nil {
		return mcp.Upstream{}, fmt.Errorf("decode server args: %w", err)
	}
	if err := json.Unmarshal([]byte(envJSON), &upstream.Env); err != nil {
		return mcp.Upstream{}, fmt.Errorf("decode server env: %w", err)
	}
	upstream.Status = mcp.ServerStatus{
		AuthType:         mcp.ServerAuthType(orDefault(authType.String, string(mcp.AuthTypeNone))),
		ConnectionStatus: mcp.ServerConnectionStatus(orDefault(connectionStatus.String, string(mcp.ConnectionStatusUnknown))),
		AuthStatus:       mcp.ServerAuthStatus(orDefault(authStatus.String, string(mcp.AuthStatusUnknown))),
		ReauthNeeded:     reauthNeeded == 1,
		LastCheckOK:      lastCheckOK == 1,
	}
	if lastCheckedAt.Valid {
		t := lastCheckedAt.Time
		upstream.Status.LastCheckedAt = &t
	}
	if lastErrorSummary.Valid {
		upstream.Status.LastErrorSummary = &lastErrorSummary.String
	}
	if actionRequired.Valid {
		upstream.Status.ActionRequired = &actionRequired.String
	}
	return upstream, nil
}

func applyInvocationFilter(builder sq.SelectBuilder, countBuilder sq.SelectBuilder, filter invocation.InvocationListFilter) (sq.SelectBuilder, sq.SelectBuilder) {
	if filter.Server != "" {
		builder = builder.Where(sq.Eq{"upstream_name": filter.Server})
		countBuilder = countBuilder.Where(sq.Eq{"upstream_name": filter.Server})
	}
	if filter.Tool != "" {
		builder = builder.Where(sq.Eq{"tool_name": filter.Tool})
		countBuilder = countBuilder.Where(sq.Eq{"tool_name": filter.Tool})
	}
	if filter.Status != "" {
		builder = builder.Where(sq.Eq{"status": filter.Status})
		countBuilder = countBuilder.Where(sq.Eq{"status": filter.Status})
	}
	return builder, countBuilder
}

func countRows(ctx context.Context, db *sql.DB, builder sq.SelectBuilder) (int, error) {
	query, args, err := builder.ToSql()
	if err != nil {
		return 0, err
	}
	var total int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func nullableString(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

func emptyToNil(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func encodeServerConfig(upstream mcp.Upstream) (string, string, error) {
	argsJSON, err := json.Marshal(upstream.Args)
	if err != nil {
		return "", "", err
	}
	envJSON, err := json.Marshal(upstream.Env)
	if err != nil {
		return "", "", err
	}
	return string(argsJSON), string(envJSON), nil
}

func ensureServerStatusColumns(db *sql.DB) error {
	statements := []string{
		`ALTER TABLE mcp_servers ADD COLUMN auth_type TEXT NOT NULL DEFAULT 'none'`,
		`ALTER TABLE mcp_servers ADD COLUMN connection_status TEXT NOT NULL DEFAULT 'unknown'`,
		`ALTER TABLE mcp_servers ADD COLUMN auth_status TEXT NOT NULL DEFAULT 'unknown'`,
		`ALTER TABLE mcp_servers ADD COLUMN reauth_needed INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE mcp_servers ADD COLUMN last_checked_at TIMESTAMP`,
		`ALTER TABLE mcp_servers ADD COLUMN last_check_ok INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE mcp_servers ADD COLUMN last_error_summary TEXT`,
		`ALTER TABLE mcp_servers ADD COLUMN action_required TEXT`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
			return err
		}
	}
	return nil
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func orDefault(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
