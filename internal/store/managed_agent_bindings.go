package store

import (
	"context"
	"database/sql"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
)

// ManagedAgentBinding links one Atryum agent record to one Claude Managed Agent
// in a configured Anthropic account. Sessions are still watched separately; this
// binding is the durable agent-to-agent mapping used by the UI and discovery.
type ManagedAgentBinding struct {
	ID                 string
	AgentCUID          string
	Account            string
	ClaudeAgentID      string
	ClaudeAgentName    string
	ClaudeAgentModel   string
	ClaudeAgentVersion int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ManagedAgentBindingRepo provides CRUD operations for managed_agent_bindings.
type ManagedAgentBindingRepo struct {
	db *sql.DB
	sb sq.StatementBuilderType
}

func NewManagedAgentBindingRepo(db *sql.DB) *ManagedAgentBindingRepo {
	return NewManagedAgentBindingRepoWithDialect(db, DialectSQLite)
}

func NewManagedAgentBindingRepoWithDialect(db *sql.DB, dialect Dialect) *ManagedAgentBindingRepo {
	return &ManagedAgentBindingRepo{db: db, sb: statementBuilderForDialect(dialect)}
}

var managedAgentBindingColumns = []string{
	"id", "agent_cuid", "account", "claude_agent_id", "claude_agent_name",
	"claude_agent_model", "claude_agent_version", "created_at", "updated_at",
}

func (r *ManagedAgentBindingRepo) ListByAgent(ctx context.Context, agentCUID string) ([]ManagedAgentBinding, error) {
	query, args, err := r.sb.Select(managedAgentBindingColumns...).
		From("managed_agent_bindings").
		Where(sq.Eq{"agent_cuid": agentCUID}).
		OrderBy("account ASC", "claude_agent_name ASC", "claude_agent_id ASC").
		ToSql()
	if err != nil {
		return nil, err
	}
	return r.list(ctx, query, args...)
}

func (r *ManagedAgentBindingRepo) List(ctx context.Context) ([]ManagedAgentBinding, error) {
	query, args, err := r.sb.Select(managedAgentBindingColumns...).
		From("managed_agent_bindings").
		OrderBy("account ASC", "claude_agent_name ASC", "claude_agent_id ASC").
		ToSql()
	if err != nil {
		return nil, err
	}
	return r.list(ctx, query, args...)
}

func (r *ManagedAgentBindingRepo) GetByClaudeAgentID(ctx context.Context, account, claudeAgentID string) (ManagedAgentBinding, error) {
	b := r.sb.Select(managedAgentBindingColumns...).
		From("managed_agent_bindings").
		Where(sq.Eq{"claude_agent_id": claudeAgentID})
	if account != "" {
		b = b.Where(sq.Eq{"account": account})
	}
	query, args, err := b.OrderBy("updated_at DESC").Limit(1).ToSql()
	if err != nil {
		return ManagedAgentBinding{}, err
	}
	return scanManagedAgentBinding(r.db.QueryRowContext(ctx, query, args...))
}

// ReplaceForAgent atomically replaces all Claude Managed Agent bindings for an
// Atryum agent. This matches the edit-modal save semantics.
func (r *ManagedAgentBindingRepo) ReplaceForAgent(ctx context.Context, agentCUID string, bindings []ManagedAgentBinding) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	deleteQuery, deleteArgs, err := r.sb.Delete("managed_agent_bindings").Where(sq.Eq{"agent_cuid": agentCUID}).ToSql()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, deleteQuery, deleteArgs...); err != nil {
		return err
	}

	now := time.Now().UTC()
	for _, b := range bindings {
		if b.ID == "" {
			b.ID = uuid.NewString()
		}
		if b.Account == "" {
			b.Account = "default"
		}
		if b.CreatedAt.IsZero() {
			b.CreatedAt = now
		}
		b.UpdatedAt = now
		conflictDeleteQuery, conflictDeleteArgs, err := r.sb.Delete("managed_agent_bindings").
			Where(sq.Eq{"account": b.Account, "claude_agent_id": b.ClaudeAgentID}).
			Where(sq.NotEq{"agent_cuid": agentCUID}).
			ToSql()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, conflictDeleteQuery, conflictDeleteArgs...); err != nil {
			return err
		}
		query, args, err := r.sb.Insert("managed_agent_bindings").
			Columns(managedAgentBindingColumns...).
			Values(b.ID, agentCUID, b.Account, b.ClaudeAgentID, b.ClaudeAgentName, b.ClaudeAgentModel, b.ClaudeAgentVersion, b.CreatedAt, b.UpdatedAt).
			ToSql()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *ManagedAgentBindingRepo) list(ctx context.Context, query string, args ...any) ([]ManagedAgentBinding, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ManagedAgentBinding
	for rows.Next() {
		b, err := scanManagedAgentBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func scanManagedAgentBinding(row interface{ Scan(dest ...any) error }) (ManagedAgentBinding, error) {
	var b ManagedAgentBinding
	if err := row.Scan(
		&b.ID,
		&b.AgentCUID,
		&b.Account,
		&b.ClaudeAgentID,
		&b.ClaudeAgentName,
		&b.ClaudeAgentModel,
		&b.ClaudeAgentVersion,
		&b.CreatedAt,
		&b.UpdatedAt,
	); err != nil {
		return ManagedAgentBinding{}, err
	}
	return b, nil
}
