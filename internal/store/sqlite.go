package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"

	"atryum/internal/invocation"
	"atryum/internal/mcp"
)

type InvocationRepo struct {
	db *sql.DB
	sb sq.StatementBuilderType
}
type EventRepo struct {
	db *sql.DB
	sb sq.StatementBuilderType
}
type ServerRepo struct {
	db *sql.DB
	sb sq.StatementBuilderType
}
type OAuthRepo struct {
	db *sql.DB
	sb sq.StatementBuilderType
}

type OAuthCredential struct {
	ServerName   string
	AccessToken  string
	RefreshToken string
	TokenType    string
	Scope        string
	ExpiresAt    *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type OAuthConnectSession struct {
	State        string
	ServerName   string
	Status       string
	CodeVerifier string
	RedirectURI  string
	StartedAt    time.Time
	CompletedAt  *time.Time
	ErrorMessage *string
}

func NewInvocationRepo(db *sql.DB) *InvocationRepo {
	return NewInvocationRepoWithDialect(db, DialectSQLite)
}
func NewEventRepo(db *sql.DB) *EventRepo   { return NewEventRepoWithDialect(db, DialectSQLite) }
func NewServerRepo(db *sql.DB) *ServerRepo { return NewServerRepoWithDialect(db, DialectSQLite) }
func NewOAuthRepo(db *sql.DB) *OAuthRepo   { return NewOAuthRepoWithDialect(db, DialectSQLite) }

func NewInvocationRepoWithDialect(db *sql.DB, dialect Dialect) *InvocationRepo {
	return &InvocationRepo{db: db, sb: statementBuilderForDialect(dialect)}
}
func NewEventRepoWithDialect(db *sql.DB, dialect Dialect) *EventRepo {
	return &EventRepo{db: db, sb: statementBuilderForDialect(dialect)}
}
func NewServerRepoWithDialect(db *sql.DB, dialect Dialect) *ServerRepo {
	return &ServerRepo{db: db, sb: statementBuilderForDialect(dialect)}
}
func NewOAuthRepoWithDialect(db *sql.DB, dialect Dialect) *OAuthRepo {
	return &OAuthRepo{db: db, sb: statementBuilderForDialect(dialect)}
}

// InitDB is now handled by migrations.go which runs embedded SQL files
// from internal/store/migrations/ in order.

func (r *InvocationRepo) Create(ctx context.Context, inv invocation.Invocation) error {
	var approval any
	if inv.Approval != nil {
		b, _ := json.Marshal(inv.Approval)
		approval = string(b)
	}
	query, args, err := r.sb.Insert("invocations").Columns(
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
	query, args, err := r.sb.Update("invocations").Set("status", inv.Status).Set("approval_json", approval).Set("response_json", nullableString(inv.Response)).Set("error_json", nullableString(inv.Error)).Set("completed_at", inv.CompletedAt).Where(sq.Eq{"invocation_id": inv.InvocationID}).ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *InvocationRepo) Get(ctx context.Context, id string) (invocation.Invocation, error) {
	query, args, err := r.sb.Select("invocation_id", "request_id", "idempotency_key", "tool_name", "upstream_name", "status", "approval_json", "request_json", "response_json", "error_json", "submitted_at", "completed_at").From("invocations").Where(sq.Eq{"invocation_id": id}).ToSql()
	if err != nil {
		return invocation.Invocation{}, err
	}
	row := r.db.QueryRowContext(ctx, query, args...)
	return scanInvocation(row)
}

func (r *InvocationRepo) GetByIdempotencyKey(ctx context.Context, key string) (invocation.Invocation, error) {
	query, args, err := r.sb.Select("invocation_id", "request_id", "idempotency_key", "tool_name", "upstream_name", "status", "approval_json", "request_json", "response_json", "error_json", "submitted_at", "completed_at").From("invocations").Where(sq.Eq{"idempotency_key": key}).ToSql()
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
	builder := r.sb.Select("invocation_id", "request_id", "idempotency_key", "tool_name", "upstream_name", "status", "approval_json", "request_json", "response_json", "error_json", "submitted_at", "completed_at").From("invocations")
	countBuilder := r.sb.Select("COUNT(*)").From("invocations")
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
	query, args, err := r.sb.Insert("invocation_events").Columns("invocation_id", "event_type", "payload_json", "created_at").Values(evt.InvocationID, evt.EventType, string(evt.Payload), evt.CreatedAt).ToSql()
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
	builder := r.sb.Select("id", "invocation_id", "event_type", "payload_json", "created_at").From("invocation_events").Where(sq.Eq{"invocation_id": invocationID})
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
	countBuilder := r.sb.Select("COUNT(*)").From("invocation_events").Where(sq.Eq{"invocation_id": invocationID})
	total, err := countRows(ctx, r.db, countBuilder)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (r *ServerRepo) CountServers(ctx context.Context) (int, error) {
	query, args, err := r.sb.Select("COUNT(*)").From("mcp_servers").ToSql()
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
	query, args, err := r.sb.Insert("mcp_servers").Columns(
		"name", "mode", "base_url", "auth_token", "timeout_seconds", "command", "args_json", "env_json", "enabled", "auth_type", "connection_status", "auth_status", "reauth_needed", "last_checked_at", "last_check_ok", "last_error_summary", "action_required", "oauth_provider_id", "oauth_provider_label", "oauth_authorize_url", "oauth_token_url", "oauth_client_id", "oauth_client_secret", "oauth_scopes", "created_at", "updated_at",
	).Values(
		upstream.Name, string(upstream.Mode), emptyToNil(upstream.BaseURL), emptyToNil(upstream.AuthToken), int(upstream.Timeout/time.Second), emptyToNil(upstream.Command), argsJSON, envJSON, boolToInt(upstream.Enabled), string(upstream.Status.AuthType), string(upstream.Status.ConnectionStatus), string(upstream.Status.AuthStatus), boolToInt(upstream.Status.ReauthNeeded), upstream.Status.LastCheckedAt, boolToInt(upstream.Status.LastCheckOK), emptyToNil(derefString(upstream.Status.LastErrorSummary)), emptyToNil(derefString(upstream.Status.ActionRequired)), emptyToNil(upstream.OAuthProviderID), emptyToNil(upstream.OAuthProviderLabel), emptyToNil(upstream.OAuthAuthorizeURL), emptyToNil(upstream.OAuthTokenURL), emptyToNil(upstream.OAuthClientID), emptyToNil(upstream.OAuthClientSecret), emptyToNil(upstream.OAuthScopes), now, now,
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

	updateMap := map[string]interface{}{
		"mode":                 string(upstream.Mode),
		"base_url":             emptyToNil(upstream.BaseURL),
		"auth_token":           emptyToNil(upstream.AuthToken),
		"timeout_seconds":      int(upstream.Timeout / time.Second),
		"command":              emptyToNil(upstream.Command),
		"args_json":            argsJSON,
		"env_json":             envJSON,
		"enabled":              boolToInt(upstream.Enabled),
		"auth_type":            string(upstream.Status.AuthType),
		"connection_status":    string(upstream.Status.ConnectionStatus),
		"auth_status":          string(upstream.Status.AuthStatus),
		"reauth_needed":        boolToInt(upstream.Status.ReauthNeeded),
		"last_checked_at":      upstream.Status.LastCheckedAt,
		"last_check_ok":        boolToInt(upstream.Status.LastCheckOK),
		"last_error_summary":   emptyToNil(derefString(upstream.Status.LastErrorSummary)),
		"action_required":      emptyToNil(derefString(upstream.Status.ActionRequired)),
		"oauth_provider_id":    emptyToNil(upstream.OAuthProviderID),
		"oauth_provider_label": emptyToNil(upstream.OAuthProviderLabel),
		"oauth_authorize_url":  emptyToNil(upstream.OAuthAuthorizeURL),
		"oauth_token_url":      emptyToNil(upstream.OAuthTokenURL),
		"oauth_client_id":      emptyToNil(upstream.OAuthClientID),
		"oauth_client_secret":  emptyToNil(upstream.OAuthClientSecret),
		"oauth_scopes":         emptyToNil(upstream.OAuthScopes),
		"updated_at":           now,
	}

	query, args, err := r.sb.Insert("mcp_servers").
		Columns(
			"name", "mode", "base_url", "auth_token", "timeout_seconds", "command", "args_json", "env_json", "enabled",
			"auth_type", "connection_status", "auth_status", "reauth_needed", "last_checked_at", "last_check_ok", "last_error_summary", "action_required",
			"oauth_provider_id", "oauth_provider_label", "oauth_authorize_url", "oauth_token_url", "oauth_client_id", "oauth_client_secret", "oauth_scopes",
			"created_at", "updated_at",
		).
		Values(
			upstream.Name, updateMap["mode"], updateMap["base_url"], updateMap["auth_token"], updateMap["timeout_seconds"],
			updateMap["command"], argsJSON, envJSON, updateMap["enabled"],
			updateMap["auth_type"], updateMap["connection_status"], updateMap["auth_status"], updateMap["reauth_needed"],
			updateMap["last_checked_at"], updateMap["last_check_ok"], updateMap["last_error_summary"], updateMap["action_required"],
			updateMap["oauth_provider_id"], updateMap["oauth_provider_label"], updateMap["oauth_authorize_url"], updateMap["oauth_token_url"],
			updateMap["oauth_client_id"], updateMap["oauth_client_secret"], updateMap["oauth_scopes"],
			now, now,
		).
		Suffix("ON CONFLICT(name) DO UPDATE SET mode = excluded.mode, base_url = excluded.base_url, auth_token = excluded.auth_token, timeout_seconds = excluded.timeout_seconds, command = excluded.command, args_json = excluded.args_json, env_json = excluded.env_json, enabled = excluded.enabled, auth_type = excluded.auth_type, connection_status = excluded.connection_status, auth_status = excluded.auth_status, reauth_needed = excluded.reauth_needed, last_checked_at = excluded.last_checked_at, last_check_ok = excluded.last_check_ok, last_error_summary = excluded.last_error_summary, action_required = excluded.action_required, oauth_provider_id = excluded.oauth_provider_id, oauth_provider_label = excluded.oauth_provider_label, oauth_authorize_url = excluded.oauth_authorize_url, oauth_token_url = excluded.oauth_token_url, oauth_client_id = excluded.oauth_client_id, oauth_client_secret = excluded.oauth_client_secret, oauth_scopes = excluded.oauth_scopes, updated_at = excluded.updated_at").
		ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *ServerRepo) UpdateServerStatus(ctx context.Context, name string, status mcp.ServerStatus) error {
	query, args, err := r.sb.Update("mcp_servers").
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
	query, args, err := r.sb.Select("name", "mode", "base_url", "auth_token", "timeout_seconds", "command", "args_json", "env_json", "enabled", "auth_type", "connection_status", "auth_status", "reauth_needed", "last_checked_at", "last_check_ok", "last_error_summary", "action_required", "oauth_provider_id", "oauth_provider_label", "oauth_authorize_url", "oauth_token_url", "oauth_client_id", "oauth_client_secret", "oauth_scopes").From("mcp_servers").Where(sq.Eq{"name": name, "enabled": 1}).ToSql()
	if err != nil {
		return mcp.Upstream{}, err
	}
	row := r.db.QueryRowContext(ctx, query, args...)
	return scanServer(row)
}

func (r *ServerRepo) GetServerAny(ctx context.Context, name string) (mcp.Upstream, error) {
	query, args, err := r.sb.Select("name", "mode", "base_url", "auth_token", "timeout_seconds", "command", "args_json", "env_json", "enabled", "auth_type", "connection_status", "auth_status", "reauth_needed", "last_checked_at", "last_check_ok", "last_error_summary", "action_required", "oauth_provider_id", "oauth_provider_label", "oauth_authorize_url", "oauth_token_url", "oauth_client_id", "oauth_client_secret", "oauth_scopes").From("mcp_servers").Where(sq.Eq{"name": name}).ToSql()
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
	builder := r.sb.Select("name", "mode", "base_url", "auth_token", "timeout_seconds", "command", "args_json", "env_json", "enabled", "auth_type", "connection_status", "auth_status", "reauth_needed", "last_checked_at", "last_check_ok", "last_error_summary", "action_required", "oauth_provider_id", "oauth_provider_label", "oauth_authorize_url", "oauth_token_url", "oauth_client_id", "oauth_client_secret", "oauth_scopes").From("mcp_servers")
	countBuilder := r.sb.Select("COUNT(*)").From("mcp_servers")
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
	query, args, err := r.sb.Delete("mcp_servers").Where(sq.Eq{"name": name}).ToSql()
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
	query, args, err := r.sb.Update("mcp_servers").Set("enabled", 0).Set("connection_status", string(mcp.ConnectionStatusDisabled)).Set("updated_at", time.Now().UTC()).Where(sq.Eq{"name": name}).ToSql()
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

func (r *OAuthRepo) UpsertCredential(ctx context.Context, cred OAuthCredential) error {
	now := time.Now().UTC()
	if cred.CreatedAt.IsZero() {
		cred.CreatedAt = now
	}
	cred.UpdatedAt = now
	query, args, err := r.sb.Insert("oauth_credentials").
		Columns("server_name", "access_token", "refresh_token", "token_type", "scope", "expires_at", "created_at", "updated_at").
		Values(cred.ServerName, cred.AccessToken, emptyToNil(cred.RefreshToken), emptyToNil(cred.TokenType), emptyToNil(cred.Scope), cred.ExpiresAt, cred.CreatedAt, cred.UpdatedAt).
		Suffix("ON CONFLICT(server_name) DO UPDATE SET access_token = excluded.access_token, refresh_token = excluded.refresh_token, token_type = excluded.token_type, scope = excluded.scope, expires_at = excluded.expires_at, updated_at = excluded.updated_at").
		ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *OAuthRepo) GetCredential(ctx context.Context, serverName string) (OAuthCredential, error) {
	query, args, err := r.sb.Select("server_name", "access_token", "refresh_token", "token_type", "scope", "expires_at", "created_at", "updated_at").
		From("oauth_credentials").Where(sq.Eq{"server_name": serverName}).ToSql()
	if err != nil {
		return OAuthCredential{}, err
	}
	row := r.db.QueryRowContext(ctx, query, args...)
	var cred OAuthCredential
	var refreshToken, tokenType, scope sql.NullString
	var expiresAt sql.NullTime
	if err := row.Scan(&cred.ServerName, &cred.AccessToken, &refreshToken, &tokenType, &scope, &expiresAt, &cred.CreatedAt, &cred.UpdatedAt); err != nil {
		return OAuthCredential{}, err
	}
	cred.RefreshToken = refreshToken.String
	cred.TokenType = tokenType.String
	cred.Scope = scope.String
	if expiresAt.Valid {
		t := expiresAt.Time
		cred.ExpiresAt = &t
	}
	return cred, nil
}

func (r *OAuthRepo) DeleteCredential(ctx context.Context, serverName string) error {
	query, args, err := r.sb.Delete("oauth_credentials").Where(sq.Eq{"server_name": serverName}).ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *OAuthRepo) UpsertConnectSession(ctx context.Context, session OAuthConnectSession) error {
	query, args, err := r.sb.Insert("oauth_connect_sessions").
		Columns("state", "server_name", "status", "code_verifier", "redirect_uri", "started_at", "completed_at", "error_message").
		Values(session.State, session.ServerName, session.Status, emptyToNil(session.CodeVerifier), session.RedirectURI, session.StartedAt, session.CompletedAt, emptyToNil(derefString(session.ErrorMessage))).
		Suffix("ON CONFLICT(state) DO UPDATE SET status = excluded.status, code_verifier = excluded.code_verifier, redirect_uri = excluded.redirect_uri, completed_at = excluded.completed_at, error_message = excluded.error_message").
		ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *OAuthRepo) GetConnectSession(ctx context.Context, state string) (OAuthConnectSession, error) {
	query, args, err := r.sb.Select("state", "server_name", "status", "code_verifier", "redirect_uri", "started_at", "completed_at", "error_message").
		From("oauth_connect_sessions").Where(sq.Eq{"state": state}).ToSql()
	if err != nil {
		return OAuthConnectSession{}, err
	}
	row := r.db.QueryRowContext(ctx, query, args...)
	var session OAuthConnectSession
	var codeVerifier, errorMessage sql.NullString
	var completedAt sql.NullTime
	if err := row.Scan(&session.State, &session.ServerName, &session.Status, &codeVerifier, &session.RedirectURI, &session.StartedAt, &completedAt, &errorMessage); err != nil {
		return OAuthConnectSession{}, err
	}
	session.CodeVerifier = codeVerifier.String
	if completedAt.Valid {
		t := completedAt.Time
		session.CompletedAt = &t
	}
	if errorMessage.Valid {
		session.ErrorMessage = &errorMessage.String
	}
	return session, nil
}

func (r *OAuthRepo) GetLatestConnectSessionByServer(ctx context.Context, serverName string) (OAuthConnectSession, error) {
	query, args, err := r.sb.Select("state", "server_name", "status", "code_verifier", "redirect_uri", "started_at", "completed_at", "error_message").
		From("oauth_connect_sessions").Where(sq.Eq{"server_name": serverName}).OrderBy("started_at DESC").Limit(1).ToSql()
	if err != nil {
		return OAuthConnectSession{}, err
	}
	row := r.db.QueryRowContext(ctx, query, args...)
	var session OAuthConnectSession
	var codeVerifier, errorMessage sql.NullString
	var completedAt sql.NullTime
	if err := row.Scan(&session.State, &session.ServerName, &session.Status, &codeVerifier, &session.RedirectURI, &session.StartedAt, &completedAt, &errorMessage); err != nil {
		return OAuthConnectSession{}, err
	}
	session.CodeVerifier = codeVerifier.String
	if completedAt.Valid {
		t := completedAt.Time
		session.CompletedAt = &t
	}
	if errorMessage.Valid {
		session.ErrorMessage = &errorMessage.String
	}
	return session, nil
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
	var oauthProviderID, oauthProviderLabel, oauthAuthorizeURL, oauthTokenURL, oauthClientID, oauthClientSecret, oauthScopes sql.NullString
	if err := scanner.Scan(&upstream.Name, &mode, &baseURL, &authToken, &timeoutSeconds, &command, &argsJSON, &envJSON, &enabled, &authType, &connectionStatus, &authStatus, &reauthNeeded, &lastCheckedAt, &lastCheckOK, &lastErrorSummary, &actionRequired, &oauthProviderID, &oauthProviderLabel, &oauthAuthorizeURL, &oauthTokenURL, &oauthClientID, &oauthClientSecret, &oauthScopes); err != nil {
		return mcp.Upstream{}, err
	}
	upstream.Mode = mcp.UpstreamMode(mode)
	upstream.BaseURL = baseURL.String
	upstream.AuthToken = authToken.String
	upstream.Timeout = time.Duration(timeoutSeconds) * time.Second
	upstream.Command = command.String
	upstream.Enabled = enabled == 1
	upstream.OAuthProviderID = oauthProviderID.String
	upstream.OAuthProviderLabel = oauthProviderLabel.String
	upstream.OAuthAuthorizeURL = oauthAuthorizeURL.String
	upstream.OAuthTokenURL = oauthTokenURL.String
	upstream.OAuthClientID = oauthClientID.String
	upstream.OAuthClientSecret = oauthClientSecret.String
	upstream.OAuthScopes = oauthScopes.String
	if err := json.Unmarshal([]byte(argsJSON), &upstream.Args); err != nil {
		return mcp.Upstream{}, fmt.Errorf("decode server args: %w", err)
	}
	if err := json.Unmarshal([]byte(envJSON), &upstream.Env); err != nil {
		return mcp.Upstream{}, fmt.Errorf("decode server env: %w", err)
	}
	if upstream.Mode == mcp.UpstreamModeHTTP {
		upstream.AuthHeaders = mcp.DecodeAuthHeaders(upstream.Env)
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
	envPayload := upstream.Env
	if upstream.Mode == mcp.UpstreamModeHTTP && len(upstream.AuthHeaders) > 0 {
		envPayload = mcp.EncodeAuthHeaders(upstream.AuthHeaders)
	}
	envJSON, err := json.Marshal(envPayload)
	if err != nil {
		return "", "", err
	}
	return string(argsJSON), string(envJSON), nil
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
