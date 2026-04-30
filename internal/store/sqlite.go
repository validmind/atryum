package store

import (
	"context"
	"database/sql"
	"encoding/json"

	sq "github.com/Masterminds/squirrel"

	"atryum/internal/invocation"
)

var psql = sq.StatementBuilder.PlaceholderFormat(sq.Question)

type InvocationRepo struct{ db *sql.DB }
type EventRepo struct{ db *sql.DB }

func NewInvocationRepo(db *sql.DB) *InvocationRepo { return &InvocationRepo{db: db} }
func NewEventRepo(db *sql.DB) *EventRepo           { return &EventRepo{db: db} }

func InitDB(db *sql.DB) error {
	if _, err := db.Exec(`PRAGMA foreign_keys = ON;`); err != nil {
		return err
	}
	schema := `
	CREATE TABLE IF NOT EXISTS invocations (
		invocation_id TEXT PRIMARY KEY,
		request_id TEXT,
		idempotency_key TEXT,
		tool_name TEXT NOT NULL,
		upstream_name TEXT NOT NULL,
		status TEXT NOT NULL,
		approval_json TEXT,
		request_json TEXT NOT NULL,
		response_json TEXT,
		error_json TEXT,
		submitted_at TIMESTAMP NOT NULL,
		completed_at TIMESTAMP
	);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_invocations_idempotency_key ON invocations(idempotency_key) WHERE idempotency_key IS NOT NULL;
	CREATE TABLE IF NOT EXISTS invocation_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		invocation_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		FOREIGN KEY(invocation_id) REFERENCES invocations(invocation_id)
	);
	`
	_, err := db.Exec(schema)
	return err
}

func (r *InvocationRepo) Create(ctx context.Context, inv invocation.Invocation) error {
	var approval any
	if inv.Approval != nil {
		b, _ := json.Marshal(inv.Approval)
		approval = string(b)
	}
	query, args, err := psql.Insert("invocations").Columns(
		"invocation_id", "request_id", "idempotency_key", "tool_name", "upstream_name", "status", "approval_json", "request_json", "response_json", "error_json", "submitted_at", "completed_at",
	).Values(
		inv.InvocationID, inv.RequestID, inv.IdempotencyKey, inv.Tool, inv.Upstream, inv.Status, approval, string(inv.Input), nullableString(inv.Response), nullableString(inv.Error), inv.SubmittedAt, inv.CompletedAt,
	).ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *InvocationRepo) UpdateResult(ctx context.Context, inv invocation.Invocation) error {
	var approval any
	if inv.Approval != nil {
		b, _ := json.Marshal(inv.Approval)
		approval = string(b)
	}
	query, args, err := psql.Update("invocations").Set("status", inv.Status).Set("approval_json", approval).Set("response_json", nullableString(inv.Response)).Set("error_json", nullableString(inv.Error)).Set("completed_at", inv.CompletedAt).Where(sq.Eq{"invocation_id": inv.InvocationID}).ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *InvocationRepo) Get(ctx context.Context, id string) (invocation.Invocation, error) {
	query, args, err := psql.Select("invocation_id", "request_id", "idempotency_key", "tool_name", "upstream_name", "status", "approval_json", "request_json", "response_json", "error_json", "submitted_at", "completed_at").From("invocations").Where(sq.Eq{"invocation_id": id}).ToSql()
	if err != nil {
		return invocation.Invocation{}, err
	}
	row := r.db.QueryRowContext(ctx, query, args...)
	return scanInvocation(row)
}

func (r *InvocationRepo) GetByIdempotencyKey(ctx context.Context, key string) (invocation.Invocation, error) {
	query, args, err := psql.Select("invocation_id", "request_id", "idempotency_key", "tool_name", "upstream_name", "status", "approval_json", "request_json", "response_json", "error_json", "submitted_at", "completed_at").From("invocations").Where(sq.Eq{"idempotency_key": key}).ToSql()
	if err != nil {
		return invocation.Invocation{}, err
	}
	row := r.db.QueryRowContext(ctx, query, args...)
	return scanInvocation(row)
}

func (r *InvocationRepo) List(ctx context.Context, limit uint64) ([]invocation.Invocation, error) {
	query, args, err := psql.Select("invocation_id", "request_id", "idempotency_key", "tool_name", "upstream_name", "status", "approval_json", "request_json", "response_json", "error_json", "submitted_at", "completed_at").From("invocations").OrderBy("submitted_at DESC").Limit(limit).ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []invocation.Invocation
	for rows.Next() {
		inv, err := scanInvocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (r *EventRepo) Create(ctx context.Context, evt invocation.Event) error {
	query, args, err := psql.Insert("invocation_events").Columns("invocation_id", "event_type", "payload_json", "created_at").Values(evt.InvocationID, evt.EventType, string(evt.Payload), evt.CreatedAt).ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *EventRepo) ListByInvocation(ctx context.Context, invocationID string) ([]invocation.Event, error) {
	query, args, err := psql.Select("id", "invocation_id", "event_type", "payload_json", "created_at").From("invocation_events").Where(sq.Eq{"invocation_id": invocationID}).OrderBy("id ASC").ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []invocation.Event
	for rows.Next() {
		var evt invocation.Event
		var payload string
		if err := rows.Scan(&evt.ID, &evt.InvocationID, &evt.EventType, &payload, &evt.CreatedAt); err != nil {
			return nil, err
		}
		evt.Payload = []byte(payload)
		out = append(out, evt)
	}
	return out, rows.Err()
}

func scanInvocation(scanner interface{ Scan(dest ...any) error }) (invocation.Invocation, error) {
	var inv invocation.Invocation
	var approval sql.NullString
	var requestID, idempotencyKey sql.NullString
	var requestJSON, responseJSON, errorJSON sql.NullString
	var completedAt sql.NullTime
	if err := scanner.Scan(&inv.InvocationID, &requestID, &idempotencyKey, &inv.Tool, &inv.Upstream, &inv.Status, &approval, &requestJSON, &responseJSON, &errorJSON, &inv.SubmittedAt, &completedAt); err != nil {
		return invocation.Invocation{}, err
	}
	if requestID.Valid {
		inv.RequestID = &requestID.String
	}
	if idempotencyKey.Valid {
		inv.IdempotencyKey = &idempotencyKey.String
	}
	inv.Input = []byte(requestJSON.String)
	if responseJSON.Valid {
		inv.Response = []byte(responseJSON.String)
	}
	if errorJSON.Valid {
		inv.Error = []byte(errorJSON.String)
	}
	if completedAt.Valid {
		t := completedAt.Time
		inv.CompletedAt = &t
	}
	if approval.Valid {
		var ap invocation.Approval
		if err := json.Unmarshal([]byte(approval.String), &ap); err == nil {
			inv.Approval = &ap
		}
	}
	return inv, nil
}

func nullableString(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}
