package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
)

// APIKey is a first-party credential that acts as one agent. Only the SHA-256
// of the token is stored (KeyHash); KeyPrefix is a short display handle.
type APIKey struct {
	ID         string
	AgentID    string
	CreatedBy  string // user id; empty when issued without a user (no-auth mode)
	Name       string
	KeyHash    string
	KeyPrefix  string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
	RevokedBy  string
	// Capabilities are extra rights (auth.Capability*), e.g. "asserter" for
	// an MCP gateway. Stored comma-separated.
	Capabilities []string
	// GatewayVendor names the gateway a key belongs to (display only).
	GatewayVendor string
}

// Active reports whether the key itself is usable at time now (ignores the
// creator's disabled state, which ResolveActive checks via the users join).
func (k APIKey) Active(now time.Time) bool {
	if k.RevokedAt != nil {
		return false
	}
	if k.ExpiresAt != nil && !now.Before(*k.ExpiresAt) {
		return false
	}
	return true
}

// APIKeysRepo manages the api_keys table.
type APIKeysRepo struct {
	db *sql.DB
	sb sq.StatementBuilderType
}

func NewAPIKeysRepo(db *sql.DB) *APIKeysRepo {
	return NewAPIKeysRepoWithDialect(db, DialectSQLite)
}

func NewAPIKeysRepoWithDialect(db *sql.DB, dialect Dialect) *APIKeysRepo {
	return &APIKeysRepo{db: db, sb: statementBuilderForDialect(dialect)}
}

var apiKeyColumns = []string{
	"id", "agent_id", "created_by", "name", "key_hash", "key_prefix",
	"created_at", "expires_at", "last_used_at", "revoked_at", "revoked_by",
	"capabilities", "gateway_vendor",
}

// Create persists a new key row. The caller has already generated the token
// and computed KeyHash/KeyPrefix (see internal/auth AgentKey helpers).
func (r *APIKeysRepo) Create(ctx context.Context, key APIKey) (APIKey, error) {
	if key.ID == "" {
		key.ID = uuid.NewString()
	}
	if key.CreatedAt.IsZero() {
		key.CreatedAt = time.Now().UTC()
	}
	query, args, err := r.sb.Insert("api_keys").
		Columns(apiKeyColumns...).
		Values(key.ID, key.AgentID, emptyToNil(key.CreatedBy), key.Name, key.KeyHash, key.KeyPrefix,
			key.CreatedAt, nullableTime(key.ExpiresAt), nil, nil, nil,
			joinCapabilities(key.Capabilities), emptyToNil(key.GatewayVendor)).
		ToSql()
	if err != nil {
		return APIKey{}, err
	}
	if _, err := r.db.ExecContext(ctx, query, args...); err != nil {
		return APIKey{}, err
	}
	return r.Get(ctx, key.ID)
}

func (r *APIKeysRepo) Get(ctx context.Context, id string) (APIKey, error) {
	query, args, err := r.sb.Select(apiKeyColumns...).From("api_keys").Where(sq.Eq{"id": id}).ToSql()
	if err != nil {
		return APIKey{}, err
	}
	return scanAPIKey(r.db.QueryRowContext(ctx, query, args...))
}

// ResolveActive looks up a key by token hash and returns it only if it is
// currently valid: not revoked, not expired, and its creator (when set) is not
// disabled. Any miss returns sql.ErrNoRows so callers cannot distinguish
// "unknown", "revoked" and "creator disabled" (deliberately, to avoid leaking
// state to unauthenticated callers).
func (r *APIKeysRepo) ResolveActive(ctx context.Context, keyHash string, now time.Time) (APIKey, error) {
	cols := make([]string, 0, len(apiKeyColumns))
	for _, c := range apiKeyColumns {
		cols = append(cols, "k."+c)
	}
	query, args, err := r.sb.Select(cols...).
		From("api_keys k").
		LeftJoin("users u ON u.id = k.created_by").
		Where(sq.Eq{"k.key_hash": keyHash}).
		Where("k.revoked_at IS NULL").
		Where("u.disabled_at IS NULL").
		ToSql()
	if err != nil {
		return APIKey{}, err
	}
	key, err := scanAPIKey(r.db.QueryRowContext(ctx, query, args...))
	if err != nil {
		return APIKey{}, err
	}
	if !key.Active(now) {
		return APIKey{}, sql.ErrNoRows
	}
	return key, nil
}

// TouchLastUsed records a use of the key. Callers throttle this (the runtime
// middleware only writes when the previous value is older than a minute).
func (r *APIKeysRepo) TouchLastUsed(ctx context.Context, id string, at time.Time) error {
	query, args, err := r.sb.Update("api_keys").Set("last_used_at", at.UTC()).Where(sq.Eq{"id": id}).ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

// ListByAgent returns every key (active and revoked) for an agent, newest first.
func (r *APIKeysRepo) ListByAgent(ctx context.Context, agentID string) ([]APIKey, error) {
	query, args, err := r.sb.Select(apiKeyColumns...).From("api_keys").
		Where(sq.Eq{"agent_id": agentID}).
		OrderBy("created_at DESC", "id ASC").ToSql()
	if err != nil {
		return nil, err
	}
	return r.list(ctx, query, args...)
}

// ListByCreator returns every key a user created, newest first.
func (r *APIKeysRepo) ListByCreator(ctx context.Context, userID string) ([]APIKey, error) {
	query, args, err := r.sb.Select(apiKeyColumns...).From("api_keys").
		Where(sq.Eq{"created_by": userID}).
		OrderBy("created_at DESC", "id ASC").ToSql()
	if err != nil {
		return nil, err
	}
	return r.list(ctx, query, args...)
}

// Revoke marks a key revoked. Revoking an already-revoked key is a no-op that
// still returns nil; an unknown id returns sql.ErrNoRows.
func (r *APIKeysRepo) Revoke(ctx context.Context, id, revokedBy string) error {
	if _, err := r.Get(ctx, id); err != nil {
		return err
	}
	query, args, err := r.sb.Update("api_keys").
		Set("revoked_at", time.Now().UTC()).
		Set("revoked_by", emptyToNil(revokedBy)).
		Where(sq.Eq{"id": id}).
		Where("revoked_at IS NULL").
		ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

// RevokeByCreator revokes every active key a user created (cascade when the
// user is disabled). Returns the number of keys revoked.
func (r *APIKeysRepo) RevokeByCreator(ctx context.Context, userID, revokedBy string) (int64, error) {
	query, args, err := r.sb.Update("api_keys").
		Set("revoked_at", time.Now().UTC()).
		Set("revoked_by", emptyToNil(revokedBy)).
		Where(sq.Eq{"created_by": userID}).
		Where("revoked_at IS NULL").
		ToSql()
	if err != nil {
		return 0, err
	}
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// RevokeByCreatorForAgent revokes a user's active keys on one agent (cascade
// when the user is removed from that agent).
func (r *APIKeysRepo) RevokeByCreatorForAgent(ctx context.Context, userID, agentID, revokedBy string) (int64, error) {
	query, args, err := r.sb.Update("api_keys").
		Set("revoked_at", time.Now().UTC()).
		Set("revoked_by", emptyToNil(revokedBy)).
		Where(sq.Eq{"created_by": userID, "agent_id": agentID}).
		Where("revoked_at IS NULL").
		ToSql()
	if err != nil {
		return 0, err
	}
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (r *APIKeysRepo) list(ctx context.Context, query string, args ...any) ([]APIKey, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func scanAPIKey(row interface{ Scan(dest ...any) error }) (APIKey, error) {
	var k APIKey
	var createdBy, revokedBy, capabilities, gatewayVendor sql.NullString
	var expires, lastUsed, revoked sql.NullTime
	if err := row.Scan(&k.ID, &k.AgentID, &createdBy, &k.Name, &k.KeyHash, &k.KeyPrefix,
		&k.CreatedAt, &expires, &lastUsed, &revoked, &revokedBy, &capabilities, &gatewayVendor); err != nil {
		return APIKey{}, err
	}
	k.Capabilities = splitCapabilities(capabilities.String)
	k.GatewayVendor = gatewayVendor.String
	k.CreatedBy = createdBy.String
	k.RevokedBy = revokedBy.String
	if expires.Valid {
		t := expires.Time
		k.ExpiresAt = &t
	}
	if lastUsed.Valid {
		t := lastUsed.Time
		k.LastUsedAt = &t
	}
	if revoked.Valid {
		t := revoked.Time
		k.RevokedAt = &t
	}
	return k, nil
}

// joinCapabilities stores capabilities as a comma-separated list.
func joinCapabilities(caps []string) string {
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return strings.Join(out, ",")
}

// splitCapabilities is joinCapabilities' inverse.
func splitCapabilities(s string) []string {
	var out []string
	for _, c := range strings.Split(s, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}
