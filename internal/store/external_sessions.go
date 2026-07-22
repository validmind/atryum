package store

import (
	"context"
	"database/sql"
	"time"

	sq "github.com/Masterminds/squirrel"

	"atryum/internal/invocation"
)

// ExternalSessionRepo persists harness sessions for the Invocations API path.
// See invocation.ExternalSession for the trust model.
type ExternalSessionRepo struct {
	db *sql.DB
	sb sq.StatementBuilderType
}

func NewExternalSessionRepo(db *sql.DB) *ExternalSessionRepo {
	return NewExternalSessionRepoWithDialect(db, DialectSQLite)
}

func NewExternalSessionRepoWithDialect(db *sql.DB, dialect Dialect) *ExternalSessionRepo {
	return &ExternalSessionRepo{db: db, sb: statementBuilderForDialect(dialect)}
}

// CreateSession inserts a new session. The ID is expected to be set by the
// caller (Atryum-minted).
func (r *ExternalSessionRepo) CreateSession(ctx context.Context, s invocation.ExternalSession) error {
	now := time.Now().UTC()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	if s.LastSeenAt.IsZero() {
		s.LastSeenAt = s.CreatedAt
	}
	// ExpiresAt is owned by the invocation service (externalSessionTTL); it sets a
	// concrete value on every session it mints. We deliberately don't apply a
	// fallback TTL here so the lifetime can't silently diverge between the two
	// sites. A zero ExpiresAt persists as non-expiring (see lookupSessionForAgent).
	query, args, err := r.sb.Insert("external_sessions").
		Columns("id", "agent_id", "harness", "client_session_id", "created_at", "last_seen_at", "expires_at").
		Values(s.ID, s.AgentID, s.Harness, s.ClientSessionID, s.CreatedAt, s.LastSeenAt, s.ExpiresAt).
		ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

// GetSession returns the session by ID, or sql.ErrNoRows if it does not exist.
func (r *ExternalSessionRepo) GetSession(ctx context.Context, id string) (invocation.ExternalSession, error) {
	query, args, err := r.sb.
		Select("id", "agent_id", "harness", "client_session_id", "created_at", "last_seen_at", "expires_at").
		From("external_sessions").
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return invocation.ExternalSession{}, err
	}
	var s invocation.ExternalSession
	err = r.db.QueryRowContext(ctx, query, args...).Scan(
		&s.ID, &s.AgentID, &s.Harness, &s.ClientSessionID, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt,
	)
	if err != nil {
		return invocation.ExternalSession{}, err
	}
	return s, nil
}

// GetSessionByAgentAndClientSessionID returns the most recently created session
// matching (agent_id, client_session_id), or sql.ErrNoRows if none exists. This
// backs the submit-path get-or-create: the (agent binding, client_session_id)
// pair is the lookup key. Newest-first ordering makes a benign double-create
// under a first-submit race resolve to the latest row — both rows are valid
// audit records; see Service.getOrCreateSession.
func (r *ExternalSessionRepo) GetSessionByAgentAndClientSessionID(ctx context.Context, agentID, clientSessionID string) (invocation.ExternalSession, error) {
	query, args, err := r.sb.
		Select("id", "agent_id", "harness", "client_session_id", "created_at", "last_seen_at", "expires_at").
		From("external_sessions").
		Where(sq.Eq{"agent_id": agentID, "client_session_id": clientSessionID}).
		OrderBy("created_at DESC").
		Limit(1).
		ToSql()
	if err != nil {
		return invocation.ExternalSession{}, err
	}
	var s invocation.ExternalSession
	err = r.db.QueryRowContext(ctx, query, args...).Scan(
		&s.ID, &s.AgentID, &s.Harness, &s.ClientSessionID, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt,
	)
	if err != nil {
		return invocation.ExternalSession{}, err
	}
	return s, nil
}

// TouchSession updates last_seen_at to now and slides expires_at to the given
// time. Best-effort; callers may ignore the error.
func (r *ExternalSessionRepo) TouchSession(ctx context.Context, id string, expiresAt time.Time) error {
	query, args, err := r.sb.Update("external_sessions").
		Set("last_seen_at", time.Now().UTC()).
		Set("expires_at", expiresAt).
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}
