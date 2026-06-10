package store

import (
	"context"
	"database/sql"
	"time"

	sq "github.com/Masterminds/squirrel"
)

// ManagedAgentSession is the store-level representation of a Claude Managed
// Agents session that Atryum is watching. last_event_id is the cursor of the
// most recently processed Anthropic event so the watcher can resume after a
// stream drop or restart.
type ManagedAgentSession struct {
	SessionID   string
	AgentID     string
	Description string
	LastEventID string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ManagedAgentSessionRepo provides upsert and query operations for the
// managed_agent_sessions table.
type ManagedAgentSessionRepo struct {
	db *sql.DB
	sb sq.StatementBuilderType
}

func NewManagedAgentSessionRepo(db *sql.DB) *ManagedAgentSessionRepo {
	return NewManagedAgentSessionRepoWithDialect(db, DialectSQLite)
}

func NewManagedAgentSessionRepoWithDialect(db *sql.DB, dialect Dialect) *ManagedAgentSessionRepo {
	return &ManagedAgentSessionRepo{db: db, sb: statementBuilderForDialect(dialect)}
}

// Upsert inserts a new watched session or updates the agent_id/description of
// an existing one. The last_event_id cursor is preserved on update so a
// re-registration does not rewind the watcher.
func (r *ManagedAgentSessionRepo) Upsert(ctx context.Context, s ManagedAgentSession) error {
	now := time.Now().UTC()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	query, args, err := r.sb.Insert("managed_agent_sessions").
		Columns("session_id", "agent_id", "description", "last_event_id", "created_at", "updated_at").
		Values(s.SessionID, s.AgentID, s.Description, s.LastEventID, s.CreatedAt, now).
		Suffix(`ON CONFLICT (session_id) DO UPDATE SET
			agent_id    = excluded.agent_id,
			description = excluded.description,
			updated_at  = excluded.updated_at`).
		ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *ManagedAgentSessionRepo) Get(ctx context.Context, sessionID string) (ManagedAgentSession, error) {
	query, args, err := r.sb.
		Select("session_id", "agent_id", "description", "last_event_id", "created_at", "updated_at").
		From("managed_agent_sessions").
		Where(sq.Eq{"session_id": sessionID}).
		ToSql()
	if err != nil {
		return ManagedAgentSession{}, err
	}
	row := r.db.QueryRowContext(ctx, query, args...)
	return scanManagedAgentSession(row)
}

func (r *ManagedAgentSessionRepo) List(ctx context.Context) ([]ManagedAgentSession, error) {
	query, args, err := r.sb.
		Select("session_id", "agent_id", "description", "last_event_id", "created_at", "updated_at").
		From("managed_agent_sessions").
		OrderBy("created_at ASC").
		ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ManagedAgentSession
	for rows.Next() {
		s, err := scanManagedAgentSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpdateCursor advances the last_event_id watermark for a session.
func (r *ManagedAgentSessionRepo) UpdateCursor(ctx context.Context, sessionID, lastEventID string) error {
	query, args, err := r.sb.Update("managed_agent_sessions").
		Set("last_event_id", lastEventID).
		Set("updated_at", time.Now().UTC()).
		Where(sq.Eq{"session_id": sessionID}).
		ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func scanManagedAgentSession(row interface{ Scan(dest ...any) error }) (ManagedAgentSession, error) {
	var s ManagedAgentSession
	if err := row.Scan(&s.SessionID, &s.AgentID, &s.Description, &s.LastEventID, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return ManagedAgentSession{}, err
	}
	return s, nil
}
