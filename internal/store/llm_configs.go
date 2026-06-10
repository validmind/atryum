package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
)

// LLMProvider is the supported LLM provider type.
type LLMProvider string

const (
	LLMProviderOpenAI           LLMProvider = "openai"
	LLMProviderAnthropic        LLMProvider = "anthropic"
	LLMProviderOpenAICompatible LLMProvider = "openai_compatible"
)

// LLMConfig holds the configuration for a locally-managed LLM used for
// ai_evaluation rules without requiring a ValidMind connection.
type LLMConfig struct {
	ID        string
	Name      string
	Provider  LLMProvider
	Model     string
	APIKey    string
	BaseURL   string // required for openai_compatible
	Enabled   bool
	CreatedAt time.Time
}

// LLMConfigsRepo provides CRUD access to the llm_configs table.
type LLMConfigsRepo struct {
	db      *sql.DB
	sb      sq.StatementBuilderType
	dialect Dialect
}

func NewLLMConfigsRepo(db *sql.DB) *LLMConfigsRepo {
	return NewLLMConfigsRepoWithDialect(db, DialectSQLite)
}

func NewLLMConfigsRepoWithDialect(db *sql.DB, dialect Dialect) *LLMConfigsRepo {
	return &LLMConfigsRepo{db: db, sb: statementBuilderForDialect(dialect), dialect: dialect}
}

// encodeBool returns the correct representation for the enabled column:
// SQLite uses INTEGER (0/1) while Postgres uses native BOOLEAN.
func (r *LLMConfigsRepo) encodeBool(v bool) any {
	if r.dialect == DialectPostgres {
		return v
	}
	return boolToInt(v)
}

var llmConfigColumns = []string{
	"id", "name", "provider", "model", "api_key", "base_url", "enabled", "created_at",
}

func (r *LLMConfigsRepo) List(ctx context.Context) ([]LLMConfig, error) {
	query, args, err := r.sb.Select(llmConfigColumns...).
		From("llm_configs").
		OrderBy("created_at ASC").
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("build llm_configs list: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMConfig
	for rows.Next() {
		cfg, err := scanLLMConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cfg)
	}
	return out, rows.Err()
}

func (r *LLMConfigsRepo) Get(ctx context.Context, id string) (LLMConfig, error) {
	query, args, err := r.sb.Select(llmConfigColumns...).
		From("llm_configs").
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return LLMConfig{}, fmt.Errorf("build llm_config get: %w", err)
	}
	return scanLLMConfig(r.db.QueryRowContext(ctx, query, args...))
}

func (r *LLMConfigsRepo) Create(ctx context.Context, cfg LLMConfig) error {
	query, args, err := r.sb.Insert("llm_configs").
		Columns(llmConfigColumns...).
		Values(
			cfg.ID,
			cfg.Name,
			string(cfg.Provider),
			cfg.Model,
			cfg.APIKey,
			cfg.BaseURL,
			r.encodeBool(cfg.Enabled),
			time.Now().UTC(),
		).ToSql()
	if err != nil {
		return fmt.Errorf("build llm_config create: %w", err)
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *LLMConfigsRepo) Update(ctx context.Context, cfg LLMConfig) error {
	upd := r.sb.Update("llm_configs").
		Set("name", cfg.Name).
		Set("provider", string(cfg.Provider)).
		Set("model", cfg.Model).
		Set("base_url", cfg.BaseURL).
		Set("enabled", r.encodeBool(cfg.Enabled)).
		Where(sq.Eq{"id": cfg.ID})
	// Only update api_key when a non-empty value is supplied (write-only field).
	if cfg.APIKey != "" {
		upd = upd.Set("api_key", cfg.APIKey)
	}
	query, args, err := upd.ToSql()
	if err != nil {
		return fmt.Errorf("build llm_config update: %w", err)
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

func (r *LLMConfigsRepo) Delete(ctx context.Context, id string) error {
	query, args, err := r.sb.Delete("llm_configs").
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return fmt.Errorf("build llm_config delete: %w", err)
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

func scanLLMConfig(scanner interface{ Scan(dest ...any) error }) (LLMConfig, error) {
	var cfg LLMConfig
	var provider string
	var enabledRaw any
	if err := scanner.Scan(
		&cfg.ID, &cfg.Name, &provider, &cfg.Model,
		&cfg.APIKey, &cfg.BaseURL, &enabledRaw, &cfg.CreatedAt,
	); err != nil {
		return LLMConfig{}, err
	}
	cfg.Provider = LLMProvider(provider)
	switch v := enabledRaw.(type) {
	case bool:
		cfg.Enabled = v
	case int64:
		cfg.Enabled = v == 1
	case int:
		cfg.Enabled = v == 1
	default:
		cfg.Enabled = true // safe default
	}
	return cfg, nil
}
