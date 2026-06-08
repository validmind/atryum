package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
)

// AgentRecord is the store-level representation of a synced VM inventory model.
// agent_ids and enabled are managed by Atryum after the initial sync and are
// never overwritten when re-syncing from the backend.
type AgentRecord struct {
	ID                 string
	VMOrganizationCUID string
	VMOrganizationName string
	VMCUID             string
	VMName             string
	VMDescription      string
	AgentIDs           string // JSON array, e.g. "[]" or "[\"id1\",\"id2\"]"
	Enabled            bool
	SyncedAt           time.Time
}

// AgentsRepo provides upsert and query operations for the agents table.
type AgentsRepo struct {
	db      *sql.DB
	sb      sq.StatementBuilderType
	dialect Dialect
}

func NewAgentsRepo(db *sql.DB) *AgentsRepo {
	return NewAgentsRepoWithDialect(db, DialectSQLite)
}

func NewAgentsRepoWithDialect(db *sql.DB, dialect Dialect) *AgentsRepo {
	return &AgentsRepo{db: db, sb: statementBuilderForDialect(dialect), dialect: dialect}
}

var agentColumns = []string{
	"id", "vm_organization_cuid", "vm_organization_name",
	"vm_cuid", "vm_name", "vm_description",
	"agent_ids", "enabled", "synced_at",
}

// Upsert inserts a new agent record or, on conflict with an existing vm_cuid,
// updates only the VM-sourced fields (vm_organization_cuid, vm_organization_name,
// vm_name, vm_description, synced_at). The agent_ids and enabled columns are
// never touched on update so that Atryum-managed state is preserved.
func (r *AgentsRepo) Upsert(ctx context.Context, agent AgentRecord) error {
	agentIDs := agent.AgentIDs
	if agentIDs == "" {
		agentIDs = "[]"
	}
	syncedAt := agent.SyncedAt
	if syncedAt.IsZero() {
		syncedAt = time.Now().UTC()
	}

	insert, args, err := r.sb.Insert("agents").
		Columns(agentColumns...).
		Values(
			agent.ID,
			agent.VMOrganizationCUID,
			agent.VMOrganizationName,
			agent.VMCUID,
			agent.VMName,
			emptyToNil(agent.VMDescription),
			agentIDs,
			agent.Enabled,
			syncedAt,
		).
		Suffix(`ON CONFLICT (vm_cuid) DO UPDATE SET
			vm_organization_cuid = excluded.vm_organization_cuid,
			vm_organization_name = excluded.vm_organization_name,
			vm_name              = excluded.vm_name,
			vm_description       = excluded.vm_description,
			synced_at            = excluded.synced_at`).
		ToSql()
	if err != nil {
		return fmt.Errorf("build agent upsert: %w", err)
	}
	_, err = r.db.ExecContext(ctx, insert, args...)
	return err
}

// Get returns a single agent record by its local id.
func (r *AgentsRepo) Get(ctx context.Context, id string) (AgentRecord, error) {
	query, args, err := r.sb.Select(agentColumns...).
		From("agents").
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return AgentRecord{}, err
	}
	return scanAgent(r.db.QueryRowContext(ctx, query, args...))
}

// UpdateAgentIDs replaces the agent_ids JSON array for the agent with the given id.
func (r *AgentsRepo) UpdateAgentIDs(ctx context.Context, id string, agentIDs string) error {
	query, args, err := r.sb.Update("agents").
		Set("agent_ids", agentIDs).
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return fmt.Errorf("build agent_ids update: %w", err)
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

// UpdateEnabled sets the enabled flag for the agent with the given id.
func (r *AgentsRepo) UpdateEnabled(ctx context.Context, id string, enabled bool) error {
	query, args, err := r.sb.Update("agents").
		Set("enabled", enabled).
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return fmt.Errorf("build agent update: %w", err)
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

// GetByVMCUID returns the agent record for the given ValidMind inventory model
// CUID. Returns sql.ErrNoRows when no match is found.
func (r *AgentsRepo) GetByVMCUID(ctx context.Context, vmCUID string) (AgentRecord, error) {
	query, args, err := r.sb.Select(agentColumns...).
		From("agents").
		Where(sq.Eq{"vm_cuid": vmCUID}).
		Limit(1).
		ToSql()
	if err != nil {
		return AgentRecord{}, err
	}
	return scanAgent(r.db.QueryRowContext(ctx, query, args...))
}

// GetByAgentID returns the agent record whose agent_ids JSON array contains the
// given agentID string. Returns sql.ErrNoRows when no match is found.
func (r *AgentsRepo) GetByAgentID(ctx context.Context, agentID string) (AgentRecord, error) {
	cols := strings.Join(agentColumns, ", ")
	var query string
	if r.dialect == DialectPostgres {
		// PostgreSQL: use the @> containment operator on JSONB.
		query = `SELECT ` + cols + ` FROM agents WHERE agent_ids @> to_jsonb($1::text)::jsonb LIMIT 1`
	} else {
		// SQLite: use json_each to expand the array and match the value.
		query = `SELECT ` + cols + ` FROM agents WHERE EXISTS (SELECT 1 FROM json_each(agent_ids) WHERE value = ?) LIMIT 1`
	}
	return scanAgent(r.db.QueryRowContext(ctx, query, agentID))
}

// Create inserts a new manually-created agent record. The caller is responsible
// for supplying unique id and vm_cuid values (e.g. UUIDs). vm_organization_*
// should be empty strings for manually-created agents.
func (r *AgentsRepo) Create(ctx context.Context, agent AgentRecord) error {
	agentIDs := agent.AgentIDs
	if agentIDs == "" {
		agentIDs = "[]"
	}
	syncedAt := agent.SyncedAt
	if syncedAt.IsZero() {
		syncedAt = time.Now().UTC()
	}

	insert, args, err := r.sb.Insert("agents").
		Columns(agentColumns...).
		Values(
			agent.ID,
			agent.VMOrganizationCUID,
			agent.VMOrganizationName,
			agent.VMCUID,
			agent.VMName,
			emptyToNil(agent.VMDescription),
			agentIDs,
			agent.Enabled,
			syncedAt,
		).
		ToSql()
	if err != nil {
		return fmt.Errorf("build agent create: %w", err)
	}
	_, err = r.db.ExecContext(ctx, insert, args...)
	return err
}

// UpdateMeta updates the user-visible name and description of the agent with
// the given id.
func (r *AgentsRepo) UpdateMeta(ctx context.Context, id, name, description string) error {
	query, args, err := r.sb.Update("agents").
		Set("vm_name", name).
		Set("vm_description", emptyToNil(description)).
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return fmt.Errorf("build agent meta update: %w", err)
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

// Delete removes the agent record with the given id.
func (r *AgentsRepo) Delete(ctx context.Context, id string) error {
	query, args, err := r.sb.Delete("agents").
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return fmt.Errorf("build agent delete: %w", err)
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

// DeleteAll removes every agent record from the table. Used when the sync
// org or record type changes so stale agents from the previous configuration
// do not linger.
func (r *AgentsRepo) DeleteAll(ctx context.Context) error {
	query, args, err := r.sb.Delete("agents").ToSql()
	if err != nil {
		return fmt.Errorf("build agents delete: %w", err)
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

// List returns all agent records ordered by vm_name.
func (r *AgentsRepo) List(ctx context.Context) ([]AgentRecord, error) {
	return r.list(ctx, r.sb.Select(agentColumns...).From("agents").OrderBy("vm_name ASC"))
}

// ListEnabled returns only enabled agent records ordered by vm_name.
func (r *AgentsRepo) ListEnabled(ctx context.Context) ([]AgentRecord, error) {
	return r.list(ctx, r.sb.Select(agentColumns...).From("agents").Where(sq.Eq{"enabled": true}).OrderBy("vm_name ASC"))
}

func (r *AgentsRepo) list(ctx context.Context, b sq.SelectBuilder) ([]AgentRecord, error) {
	query, args, err := b.ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentRecord
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanAgent(scanner interface{ Scan(dest ...any) error }) (AgentRecord, error) {
	var a AgentRecord
	var vmDescription sql.NullString
	if err := scanner.Scan(
		&a.ID, &a.VMOrganizationCUID, &a.VMOrganizationName,
		&a.VMCUID, &a.VMName, &vmDescription,
		&a.AgentIDs, &a.Enabled, &a.SyncedAt,
	); err != nil {
		return AgentRecord{}, err
	}
	a.VMDescription = vmDescription.String
	return a, nil
}
