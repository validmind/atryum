package store

import (
	"context"
	"database/sql"
	"time"

	sq "github.com/Masterminds/squirrel"
)

// AgentMemberRoleOwner is the only membership role in the open core. The
// column exists so embedding programs can introduce finer roles without a
// schema change.
const AgentMemberRoleOwner = "owner"

// AgentMember links a user to an agent they may operate (view, issue keys for).
type AgentMember struct {
	AgentID   string
	UserID    string
	Role      string
	CreatedAt time.Time
	// Denormalized user fields for list responses; empty when not joined.
	UserEmail string
	UserName  string
	// Denormalized agent fields for per-user list responses; empty when not joined.
	AgentName    string
	AgentEnabled bool
}

// AgentMembersRepo manages the users ↔ agents join table.
type AgentMembersRepo struct {
	db *sql.DB
	sb sq.StatementBuilderType
}

func NewAgentMembersRepo(db *sql.DB) *AgentMembersRepo {
	return NewAgentMembersRepoWithDialect(db, DialectSQLite)
}

func NewAgentMembersRepoWithDialect(db *sql.DB, dialect Dialect) *AgentMembersRepo {
	return &AgentMembersRepo{db: db, sb: statementBuilderForDialect(dialect)}
}

// Add makes user a member of agent. Adding an existing member is a no-op.
func (r *AgentMembersRepo) Add(ctx context.Context, agentID, userID, role string) error {
	if role == "" {
		role = AgentMemberRoleOwner
	}
	if _, err := r.Get(ctx, agentID, userID); err == nil {
		return nil
	} else if err != sql.ErrNoRows {
		return err
	}
	query, args, err := r.sb.Insert("agent_members").
		Columns("agent_id", "user_id", "role", "created_at").
		Values(agentID, userID, role, time.Now().UTC()).
		ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *AgentMembersRepo) Remove(ctx context.Context, agentID, userID string) error {
	query, args, err := r.sb.Delete("agent_members").
		Where(sq.Eq{"agent_id": agentID, "user_id": userID}).ToSql()
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *AgentMembersRepo) Get(ctx context.Context, agentID, userID string) (AgentMember, error) {
	query, args, err := r.sb.Select("agent_id", "user_id", "role", "created_at").
		From("agent_members").
		Where(sq.Eq{"agent_id": agentID, "user_id": userID}).ToSql()
	if err != nil {
		return AgentMember{}, err
	}
	var m AgentMember
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&m.AgentID, &m.UserID, &m.Role, &m.CreatedAt); err != nil {
		return AgentMember{}, err
	}
	return m, nil
}

// IsMember reports whether user belongs to agent.
func (r *AgentMembersRepo) IsMember(ctx context.Context, agentID, userID string) (bool, error) {
	_, err := r.Get(ctx, agentID, userID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// ListByAgent returns the members of an agent with user display fields joined.
func (r *AgentMembersRepo) ListByAgent(ctx context.Context, agentID string) ([]AgentMember, error) {
	query, args, err := r.sb.Select("m.agent_id", "m.user_id", "m.role", "m.created_at", "u.email", "u.name").
		From("agent_members m").
		Join("users u ON u.id = m.user_id").
		Where(sq.Eq{"m.agent_id": agentID}).
		OrderBy("m.created_at ASC", "m.user_id ASC").ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentMember
	for rows.Next() {
		var m AgentMember
		if err := rows.Scan(&m.AgentID, &m.UserID, &m.Role, &m.CreatedAt, &m.UserEmail, &m.UserName); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListByUser returns every membership of a user with agent display fields
// joined, oldest membership first.
func (r *AgentMembersRepo) ListByUser(ctx context.Context, userID string) ([]AgentMember, error) {
	query, args, err := r.sb.Select("m.agent_id", "m.user_id", "m.role", "m.created_at", "a.vm_name", "a.enabled").
		From("agent_members m").
		Join("agents a ON a.id = m.agent_id").
		Where(sq.Eq{"m.user_id": userID}).
		OrderBy("m.created_at ASC", "m.agent_id ASC").ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentMember
	for rows.Next() {
		var m AgentMember
		if err := rows.Scan(&m.AgentID, &m.UserID, &m.Role, &m.CreatedAt, &m.AgentName, &m.AgentEnabled); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AgentIDsForUser returns the ids of every agent the user is a member of.
func (r *AgentMembersRepo) AgentIDsForUser(ctx context.Context, userID string) ([]string, error) {
	query, args, err := r.sb.Select("agent_id").From("agent_members").
		Where(sq.Eq{"user_id": userID}).OrderBy("created_at ASC").ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RemoveAllForUser drops every membership of a user (used when disabling).
func (r *AgentMembersRepo) RemoveAllForUser(ctx context.Context, userID string) error {
	query, args, err := r.sb.Delete("agent_members").Where(sq.Eq{"user_id": userID}).ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}
