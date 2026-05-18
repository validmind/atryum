package store

import (
	"context"
	"database/sql"
	"time"

	sq "github.com/Masterminds/squirrel"
)

// ClaudeAgent is the persisted representation of a Claude Managed Agent that
// atryum is gating tool approvals for. Name doubles as the rule scope (it is
// matched against approval_rules.server_pattern) so existing rule machinery
// can target a specific agent.
type ClaudeAgent struct {
	ID          string
	Name        string
	AgentID     string
	Description string
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ClaudeAgentSession ties a Claude session id to an agent so the watcher
// knows what to poll and where to dispatch invocations.
type ClaudeAgentSession struct {
	ID              string
	AgentID         string
	SessionID       string
	LastEventCursor string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type ClaudeAgentsRepo struct {
	db *sql.DB
	sb sq.StatementBuilderType
}

func NewClaudeAgentsRepo(db *sql.DB) *ClaudeAgentsRepo {
	return NewClaudeAgentsRepoWithDialect(db, DialectSQLite)
}

func NewClaudeAgentsRepoWithDialect(db *sql.DB, dialect Dialect) *ClaudeAgentsRepo {
	return &ClaudeAgentsRepo{db: db, sb: statementBuilderForDialect(dialect)}
}

var claudeAgentColumns = []string{
	"id", "name", "agent_id", "description", "enabled", "created_at", "updated_at",
}

var claudeAgentSessionColumns = []string{
	"id", "agent_id", "session_id", "last_event_cursor", "created_at", "updated_at",
}

// CreateAgent inserts a new claude agent row.
func (r *ClaudeAgentsRepo) CreateAgent(ctx context.Context, agent ClaudeAgent) error {
	now := time.Now().UTC()
	query, args, err := r.sb.Insert("claude_agents").
		Columns(claudeAgentColumns...).
		Values(agent.ID, agent.Name, agent.AgentID, emptyToNil(agent.Description),
			agent.Enabled, now, now).
		ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *ClaudeAgentsRepo) GetAgent(ctx context.Context, id string) (ClaudeAgent, error) {
	query, args, err := r.sb.Select(claudeAgentColumns...).
		From("claude_agents").
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return ClaudeAgent{}, err
	}
	return scanClaudeAgent(r.db.QueryRowContext(ctx, query, args...))
}

func (r *ClaudeAgentsRepo) GetAgentByName(ctx context.Context, name string) (ClaudeAgent, error) {
	query, args, err := r.sb.Select(claudeAgentColumns...).
		From("claude_agents").
		Where(sq.Eq{"name": name}).
		ToSql()
	if err != nil {
		return ClaudeAgent{}, err
	}
	return scanClaudeAgent(r.db.QueryRowContext(ctx, query, args...))
}

// GetAgentByAgentID looks an agent up by its Anthropic-side agent_id (the
// `agent_xxx` value), used by the discovery loop to decide whether a row
// already exists before inserting.
func (r *ClaudeAgentsRepo) GetAgentByAgentID(ctx context.Context, agentID string) (ClaudeAgent, error) {
	query, args, err := r.sb.Select(claudeAgentColumns...).
		From("claude_agents").
		Where(sq.Eq{"agent_id": agentID}).
		ToSql()
	if err != nil {
		return ClaudeAgent{}, err
	}
	return scanClaudeAgent(r.db.QueryRowContext(ctx, query, args...))
}

func (r *ClaudeAgentsRepo) ListAgents(ctx context.Context) ([]ClaudeAgent, error) {
	query, args, err := r.sb.Select(claudeAgentColumns...).
		From("claude_agents").
		OrderBy("name ASC").
		ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClaudeAgent
	for rows.Next() {
		agent, err := scanClaudeAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, agent)
	}
	return out, rows.Err()
}

func (r *ClaudeAgentsRepo) UpdateAgent(ctx context.Context, agent ClaudeAgent) error {
	now := time.Now().UTC()
	query, args, err := r.sb.Update("claude_agents").
		Set("name", agent.Name).
		Set("agent_id", agent.AgentID).
		Set("description", emptyToNil(agent.Description)).
		Set("enabled", agent.Enabled).
		Set("updated_at", now).
		Where(sq.Eq{"id": agent.ID}).
		ToSql()
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

func (r *ClaudeAgentsRepo) DeleteAgent(ctx context.Context, id string) error {
	query, args, err := r.sb.Delete("claude_agents").
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

// CreateSession registers a Claude session id under an agent.
func (r *ClaudeAgentsRepo) CreateSession(ctx context.Context, session ClaudeAgentSession) error {
	now := time.Now().UTC()
	query, args, err := r.sb.Insert("claude_agent_sessions").
		Columns(claudeAgentSessionColumns...).
		Values(session.ID, session.AgentID, session.SessionID,
			emptyToNil(session.LastEventCursor), now, now).
		ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *ClaudeAgentsRepo) GetSession(ctx context.Context, id string) (ClaudeAgentSession, error) {
	query, args, err := r.sb.Select(claudeAgentSessionColumns...).
		From("claude_agent_sessions").
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return ClaudeAgentSession{}, err
	}
	return scanClaudeAgentSession(r.db.QueryRowContext(ctx, query, args...))
}

// GetSessionBySessionID looks up a session row by its Anthropic-side
// session_id, used by the discovery loop to decide whether a row already
// exists before inserting.
func (r *ClaudeAgentsRepo) GetSessionBySessionID(ctx context.Context, sessionID string) (ClaudeAgentSession, error) {
	query, args, err := r.sb.Select(claudeAgentSessionColumns...).
		From("claude_agent_sessions").
		Where(sq.Eq{"session_id": sessionID}).
		ToSql()
	if err != nil {
		return ClaudeAgentSession{}, err
	}
	return scanClaudeAgentSession(r.db.QueryRowContext(ctx, query, args...))
}

func (r *ClaudeAgentsRepo) ListSessionsByAgent(ctx context.Context, agentID string) ([]ClaudeAgentSession, error) {
	query, args, err := r.sb.Select(claudeAgentSessionColumns...).
		From("claude_agent_sessions").
		Where(sq.Eq{"agent_id": agentID}).
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
	var out []ClaudeAgentSession
	for rows.Next() {
		session, err := scanClaudeAgentSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, session)
	}
	return out, rows.Err()
}

// WatchableSession is a session row joined with its parent agent's name and
// enabled flag. Used by the claude agents watcher.
type WatchableSession struct {
	ID              string
	AgentID         string
	AgentName       string
	SessionID       string
	LastEventCursor string
}

// ListWatchableSessions returns sessions whose parent agent is enabled,
// joined with the agent name. Disabled agents are skipped so operators can
// pause polling without deleting rows.
func (r *ClaudeAgentsRepo) ListWatchableSessions(ctx context.Context) ([]WatchableSession, error) {
	query, args, err := r.sb.Select(
		"s.id", "s.agent_id", "a.name", "s.session_id", "s.last_event_cursor",
	).
		From("claude_agent_sessions s").
		Join("claude_agents a ON a.id = s.agent_id").
		Where(sq.Eq{"a.enabled": true}).
		OrderBy("s.created_at ASC").
		ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WatchableSession
	for rows.Next() {
		var s WatchableSession
		var cursor sql.NullString
		if err := rows.Scan(&s.ID, &s.AgentID, &s.AgentName, &s.SessionID, &cursor); err != nil {
			return nil, err
		}
		s.LastEventCursor = cursor.String
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *ClaudeAgentsRepo) ListAllSessions(ctx context.Context) ([]ClaudeAgentSession, error) {
	query, args, err := r.sb.Select(claudeAgentSessionColumns...).
		From("claude_agent_sessions").
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
	var out []ClaudeAgentSession
	for rows.Next() {
		session, err := scanClaudeAgentSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, session)
	}
	return out, rows.Err()
}

// UpdateSessionCursor updates the last_event_cursor for a session, used by
// the watcher to advance its polling position.
func (r *ClaudeAgentsRepo) UpdateSessionCursor(ctx context.Context, id, cursor string) error {
	now := time.Now().UTC()
	query, args, err := r.sb.Update("claude_agent_sessions").
		Set("last_event_cursor", emptyToNil(cursor)).
		Set("updated_at", now).
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

func (r *ClaudeAgentsRepo) DeleteSession(ctx context.Context, id string) error {
	query, args, err := r.sb.Delete("claude_agent_sessions").
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

func scanClaudeAgent(scanner interface{ Scan(dest ...any) error }) (ClaudeAgent, error) {
	var agent ClaudeAgent
	var description sql.NullString
	if err := scanner.Scan(
		&agent.ID, &agent.Name, &agent.AgentID, &description,
		&agent.Enabled, &agent.CreatedAt, &agent.UpdatedAt,
	); err != nil {
		return ClaudeAgent{}, err
	}
	agent.Description = description.String
	return agent, nil
}

func scanClaudeAgentSession(scanner interface{ Scan(dest ...any) error }) (ClaudeAgentSession, error) {
	var session ClaudeAgentSession
	var cursor sql.NullString
	if err := scanner.Scan(
		&session.ID, &session.AgentID, &session.SessionID, &cursor,
		&session.CreatedAt, &session.UpdatedAt,
	); err != nil {
		return ClaudeAgentSession{}, err
	}
	session.LastEventCursor = cursor.String
	return session, nil
}
