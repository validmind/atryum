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
	// Charter is the governing text used by local LLM-as-judge evaluation.
	// Set by humans for manually-created agents; ignored for VM-synced agents
	// (which get their charter from the ValidMind custom field at eval time).
	Charter string
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
	"agent_ids", "enabled", "synced_at", "charter",
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
			agent.Charter,
		).
		Suffix(`ON CONFLICT (vm_cuid) DO UPDATE SET
			vm_organization_cuid = excluded.vm_organization_cuid,
			vm_organization_name = excluded.vm_organization_name,
			vm_name              = excluded.vm_name,
			vm_description       = excluded.vm_description,
			charter              = excluded.charter,
			synced_at            = excluded.synced_at`).
		ToSql()
	// Note: charter is sourced from the ValidMind charter custom field and
	// refreshed on every re-sync (the UI shows it read-only, "managed by sync").
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
// ORDER BY id ensures deterministic results when multiple agents share the same
// agent_id (which is a misconfiguration, but we handle it gracefully).
func (r *AgentsRepo) GetByAgentID(ctx context.Context, agentID string) (AgentRecord, error) {
	cols := strings.Join(agentColumns, ", ")
	var query string
	if r.dialect == DialectPostgres {
		// PostgreSQL: use the @> containment operator on JSONB.
		query = `SELECT ` + cols + ` FROM agents WHERE agent_ids @> to_jsonb($1::text)::jsonb ORDER BY id LIMIT 1`
	} else {
		// SQLite: use json_each to expand the array and match the value.
		query = `SELECT ` + cols + ` FROM agents WHERE EXISTS (SELECT 1 FROM json_each(agent_ids) WHERE value = ?) ORDER BY id LIMIT 1`
	}
	return scanAgent(r.db.QueryRowContext(ctx, query, agentID))
}

// CheckAgentIDConflict checks whether any of the given agentIDs are already
// claimed by an agent other than the one with excludeID. It returns the first
// conflicting agentID and the name of the owning agent, or empty strings when
// no conflict exists. Pass an empty excludeID when creating a new agent.
func (r *AgentsRepo) CheckAgentIDConflict(ctx context.Context, excludeID string, agentIDs []string) (conflictID, ownerName string, err error) {
	for _, id := range agentIDs {
		record, rerr := r.GetByAgentID(ctx, id)
		if rerr == sql.ErrNoRows {
			continue
		}
		if rerr != nil {
			return "", "", rerr
		}
		if record.ID != excludeID {
			return id, record.VMName, nil
		}
	}
	return "", "", nil
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
			agent.Charter,
		).
		ToSql()
	if err != nil {
		return fmt.Errorf("build agent create: %w", err)
	}
	_, err = r.db.ExecContext(ctx, insert, args...)
	return err
}

// UpdateMeta updates the user-visible name, description, and charter of
// the agent with the given id.
func (r *AgentsRepo) UpdateMeta(ctx context.Context, id, name, description, charter string) error {
	query, args, err := r.sb.Update("agents").
		Set("vm_name", name).
		Set("vm_description", emptyToNil(description)).
		Set("charter", charter).
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

// DeleteSynced removes only agent records that originated from a ValidMind
// sync (vm_organization_cuid != ”). Manually-created agents (empty
// vm_organization_cuid) are preserved.
func (r *AgentsRepo) DeleteSynced(ctx context.Context) error {
	query, args, err := r.sb.Delete("agents").
		Where(sq.NotEq{"vm_organization_cuid": ""}).
		ToSql()
	if err != nil {
		return fmt.Errorf("build synced agents delete: %w", err)
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

// ListVMCUIDsWithBindings returns the vm_cuid of every agent that has at least
// one managed_agent_binding row. These agents must not be pruned during sync:
// deleting them would cascade-delete their binding configuration.
func (r *AgentsRepo) ListVMCUIDsWithBindings(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT vm_cuid FROM agents WHERE id IN (SELECT DISTINCT agent_cuid FROM managed_agent_bindings)`,
	)
	if err != nil {
		return nil, fmt.Errorf("list agents with bindings: %w", err)
	}
	defer rows.Close()
	var cuids []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cuids = append(cuids, c)
	}
	return cuids, rows.Err()
}

// DeleteSyncedStaleForOrg removes synced agent records for the given org whose
// vm_cuid is not in keepCUIDs. Called after each sync to prune agents that were
// archived or deleted in ValidMind. Manually-created agents (empty
// vm_organization_cuid) are never touched.
//
// If keepCUIDs is empty, all synced agents for the org are removed (the org has
// no active records of the configured record type).
func (r *AgentsRepo) DeleteSyncedStaleForOrg(ctx context.Context, orgCUID string, keepCUIDs []string) error {
	q := r.sb.Delete("agents").Where(sq.Eq{"vm_organization_cuid": orgCUID})
	if len(keepCUIDs) > 0 {
		q = q.Where(sq.NotEq{"vm_cuid": keepCUIDs})
	}
	query, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("build stale agent delete: %w", err)
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
		&a.AgentIDs, &a.Enabled, &a.SyncedAt, &a.Charter,
	); err != nil {
		return AgentRecord{}, err
	}
	a.VMDescription = vmDescription.String
	return a, nil
}
