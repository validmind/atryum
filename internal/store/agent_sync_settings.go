package store

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	sq "github.com/Masterminds/squirrel"
)

// AgentSyncSettings holds the three configurable values that control agent
// synchronisation from the ValidMind backend. All fields are empty strings
// when the user has not yet configured sync (i.e. no row exists yet).
type AgentSyncSettings struct {
	OrgCUID             string
	AgentRecordTypeSlug string
	ConstitutionFieldKey string
	UpdatedAt           time.Time
}

var agentSyncSettingsColumns = []string{
	"org_cuid", "agent_record_type_slug", "constitution_field_key", "updated_at",
}

// AgentSyncSettingsRepo provides read/write access to the singleton
// agent_sync_settings row.
type AgentSyncSettingsRepo struct {
	db      *sql.DB
	sb      sq.StatementBuilderType
	dialect Dialect
}

func NewAgentSyncSettingsRepo(db *sql.DB) *AgentSyncSettingsRepo {
	return NewAgentSyncSettingsRepoWithDialect(db, DialectSQLite)
}

func NewAgentSyncSettingsRepoWithDialect(db *sql.DB, dialect Dialect) *AgentSyncSettingsRepo {
	return &AgentSyncSettingsRepo{db: db, sb: statementBuilderForDialect(dialect), dialect: dialect}
}

// Get returns the current agent sync settings. Returns an empty
// AgentSyncSettings (no error) when no row has been saved yet.
func (r *AgentSyncSettingsRepo) Get(ctx context.Context) (AgentSyncSettings, error) {
	query, args, err := r.sb.Select(agentSyncSettingsColumns...).
		From("agent_sync_settings").
		Where(sq.Eq{"id": 1}).
		ToSql()
	if err != nil {
		return AgentSyncSettings{}, fmt.Errorf("build agent_sync_settings select: %w", err)
	}
	s, err := scanAgentSyncSettings(r.db.QueryRowContext(ctx, query, args...))
	if err == sql.ErrNoRows {
		return AgentSyncSettings{}, nil
	}
	return s, err
}

// Save persists all three fields using an upsert so the row is created on
// first save and updated on subsequent saves regardless of whether a row
// already exists.
func (r *AgentSyncSettingsRepo) Save(ctx context.Context, s AgentSyncSettings) error {
	now := time.Now().UTC()
	var query string
	var args []any
	if r.dialect == DialectPostgres {
		// PostgreSQL upsert
		query = `INSERT INTO agent_sync_settings (id, org_cuid, agent_record_type_slug, constitution_field_key, updated_at)
VALUES (1, $1, $2, $3, $4)
ON CONFLICT (id) DO UPDATE SET
  org_cuid               = EXCLUDED.org_cuid,
  agent_record_type_slug = EXCLUDED.agent_record_type_slug,
  constitution_field_key = EXCLUDED.constitution_field_key,
  updated_at             = EXCLUDED.updated_at`
		args = []any{s.OrgCUID, s.AgentRecordTypeSlug, s.ConstitutionFieldKey, now}
	} else {
		// SQLite: INSERT OR REPLACE replaces the row when the primary key conflicts.
		query = `INSERT OR REPLACE INTO agent_sync_settings (id, org_cuid, agent_record_type_slug, constitution_field_key, updated_at) VALUES (1, ?, ?, ?, ?)`
		args = []any{s.OrgCUID, s.AgentRecordTypeSlug, s.ConstitutionFieldKey, now}
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		log.Printf("[agent_sync_settings] Save error: %v", err)
		return err
	}
	rows, _ := result.RowsAffected()
	log.Printf("[agent_sync_settings] Save ok: rows_affected=%d org_cuid=%q record_type=%q", rows, s.OrgCUID, s.AgentRecordTypeSlug)
	return nil
}

func scanAgentSyncSettings(row interface{ Scan(dest ...any) error }) (AgentSyncSettings, error) {
	var s AgentSyncSettings
	if err := row.Scan(
		&s.OrgCUID,
		&s.AgentRecordTypeSlug,
		&s.ConstitutionFieldKey,
		&s.UpdatedAt,
	); err != nil {
		return AgentSyncSettings{}, err
	}
	return s, nil
}
