package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	sq "github.com/Masterminds/squirrel"

	"atryum/internal/invocation"
)

// AgentClient is what an agent identifies itself as via MCP `initialize`
// (clientInfo) plus the JWT-derived subject/issuer captured at the same time.
// Atryum stores the most recently observed values per agent_id so the
// invocations UI can show context like "Amp 0.0.1234", subject (user id), etc.
type AgentClient struct {
	AgentID       string
	ClientName    string
	ClientVersion string
	Subject       string
	Issuer        string
	LastSeenAt    time.Time
}

type AgentClientRepo struct {
	db      *sql.DB
	sb      sq.StatementBuilderType
	dialect Dialect
}

func NewAgentClientRepo(db *sql.DB) *AgentClientRepo {
	return NewAgentClientRepoWithDialect(db, DialectSQLite)
}

func NewAgentClientRepoWithDialect(db *sql.DB, dialect Dialect) *AgentClientRepo {
	return &AgentClientRepo{db: db, sb: statementBuilderForDialect(dialect), dialect: dialect}
}

// Upsert inserts or updates the latest-seen client info for the agent. Only
// non-empty fields on `in` overwrite the existing row (so a tools/list
// initialize that lacks clientInfo doesn't clobber a previously captured one).
func (r *AgentClientRepo) Upsert(ctx context.Context, in AgentClient) error {
	if in.AgentID == "" {
		return errors.New("agent_id is required")
	}
	if in.LastSeenAt.IsZero() {
		in.LastSeenAt = time.Now().UTC()
	}
	existing, err := r.Get(ctx, in.AgentID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	merged := AgentClient{
		AgentID:       in.AgentID,
		ClientName:    pickString(in.ClientName, existing.ClientName),
		ClientVersion: pickString(in.ClientVersion, existing.ClientVersion),
		Subject:       pickString(in.Subject, existing.Subject),
		Issuer:        pickString(in.Issuer, existing.Issuer),
		LastSeenAt:    in.LastSeenAt,
	}
	// Use delete+insert to stay dialect-agnostic for upserts.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	delQ, delArgs, err := r.sb.Delete("agent_clients").Where(sq.Eq{"agent_id": in.AgentID}).ToSql()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, delQ, delArgs...); err != nil {
		return err
	}
	insQ, insArgs, err := r.sb.Insert("agent_clients").
		Columns("agent_id", "client_name", "client_version", "subject", "issuer", "last_seen_at").
		Values(merged.AgentID, nullableStr(merged.ClientName), nullableStr(merged.ClientVersion), nullableStr(merged.Subject), nullableStr(merged.Issuer), merged.LastSeenAt).
		ToSql()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, insQ, insArgs...); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *AgentClientRepo) Get(ctx context.Context, agentID string) (AgentClient, error) {
	q, args, err := r.sb.Select("agent_id", "client_name", "client_version", "subject", "issuer", "last_seen_at").
		From("agent_clients").Where(sq.Eq{"agent_id": agentID}).ToSql()
	if err != nil {
		return AgentClient{}, err
	}
	row := r.db.QueryRowContext(ctx, q, args...)
	var out AgentClient
	var name, version, subject, issuer sql.NullString
	if err := row.Scan(&out.AgentID, &name, &version, &subject, &issuer, &out.LastSeenAt); err != nil {
		return AgentClient{}, err
	}
	out.ClientName = name.String
	out.ClientVersion = version.String
	out.Subject = subject.String
	out.Issuer = issuer.String
	return out, nil
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func pickString(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

// RecordAgentClient satisfies the handler-side agentClientRecorder interface.
func (r *AgentClientRepo) RecordAgentClient(ctx context.Context, in AgentClient) error {
	return r.Upsert(ctx, in)
}

// LookupAgentClient satisfies the invocation.Service agentClientLookup
// interface and returns the captured clientInfo + JWT subject for the agent.
// Missing rows / errors yield (zero, false) so the caller can leave the
// optional response fields unset.
func (r *AgentClientRepo) LookupAgentClient(ctx context.Context, agentID string) (invocation.AgentClientInfo, bool) {
	if agentID == "" {
		return invocation.AgentClientInfo{}, false
	}
	row, err := r.Get(ctx, agentID)
	if err != nil {
		return invocation.AgentClientInfo{}, false
	}
	return invocation.AgentClientInfo{
		ClientName:    row.ClientName,
		ClientVersion: row.ClientVersion,
		Subject:       row.Subject,
	}, true
}
