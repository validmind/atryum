package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	sq "github.com/Masterminds/squirrel"

	"atryum/internal/invocation"
)

// PlansRepo persists agent-submitted plans. It returns invocation-layer
// domain types directly (same pattern as InvocationRepo) to avoid an
// import cycle.
type PlansRepo struct {
	db *sql.DB
	sb sq.StatementBuilderType
}

func NewPlansRepo(db *sql.DB) *PlansRepo {
	return NewPlansRepoWithDialect(db, DialectSQLite)
}

func NewPlansRepoWithDialect(db *sql.DB, dialect Dialect) *PlansRepo {
	return &PlansRepo{db: db, sb: statementBuilderForDialect(dialect)}
}

var planColumns = []string{
	"plan_id", "agent_id", "source", "thread_id", "goal", "rationale",
	"actions_json", "status", "approval_json", "matched_rule_id", "feedback",
	"parent_plan_id", "revision", "ttl_seconds", "client_name", "client_version",
	"expires_at", "submitted_at", "decided_at",
}

func (r *PlansRepo) Create(ctx context.Context, p invocation.Plan) error {
	actionsJSON, approvalJSON, err := encodePlanJSON(p)
	if err != nil {
		return err
	}
	query, args, err := r.sb.Insert("plans").
		Columns(planColumns...).
		Values(
			p.PlanID, p.AgentID, p.Source, p.ThreadID, p.Goal, p.Rationale,
			actionsJSON, p.Status, approvalJSON, p.MatchedRuleID, p.Feedback,
			p.ParentPlanID, p.Revision, p.TTLSeconds, p.ClientName, p.ClientVersion,
			p.ExpiresAt, p.SubmittedAt, p.DecidedAt,
		).ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

// Update persists the mutable review fields of a plan: status, approval,
// feedback, matched rule, TTL, expiry, and decision time.
func (r *PlansRepo) Update(ctx context.Context, p invocation.Plan) error {
	_, approvalJSON, err := encodePlanJSON(p)
	if err != nil {
		return err
	}
	query, args, err := r.sb.Update("plans").
		Set("status", p.Status).
		Set("approval_json", approvalJSON).
		Set("matched_rule_id", p.MatchedRuleID).
		Set("feedback", p.Feedback).
		Set("ttl_seconds", p.TTLSeconds).
		Set("expires_at", p.ExpiresAt).
		Set("decided_at", p.DecidedAt).
		Set("updated_at", time.Now().UTC()).
		Where(sq.Eq{"plan_id": p.PlanID}).
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

func (r *PlansRepo) Get(ctx context.Context, id string) (invocation.Plan, error) {
	query, args, err := r.sb.Select(planColumns...).
		From("plans").
		Where(sq.Eq{"plan_id": id}).
		ToSql()
	if err != nil {
		return invocation.Plan{}, err
	}
	return scanPlan(r.db.QueryRowContext(ctx, query, args...))
}

func (r *PlansRepo) List(ctx context.Context, filter invocation.PlanListFilter) ([]invocation.Plan, int, error) {
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	builder := r.sb.Select(planColumns...).From("plans")
	countBuilder := r.sb.Select("COUNT(*)").From("plans")
	if filter.Status != "" {
		builder = builder.Where(sq.Eq{"status": filter.Status})
		countBuilder = countBuilder.Where(sq.Eq{"status": filter.Status})
	}
	if filter.AgentID != "" {
		builder = builder.Where(sq.Eq{"agent_id": filter.AgentID})
		countBuilder = countBuilder.Where(sq.Eq{"agent_id": filter.AgentID})
	}
	query, args, err := builder.OrderBy("submitted_at DESC").Limit(filter.Limit).Offset(filter.Offset).ToSql()
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []invocation.Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	total, err := countRows(ctx, r.db, countBuilder)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// ListActiveByAgent returns the approved plans owned by any of the given
// agent ids, newest first. Expiry is enforced lazily by the caller.
func (r *PlansRepo) ListActiveByAgent(ctx context.Context, agentIDs []string) ([]invocation.Plan, error) {
	if len(agentIDs) == 0 {
		return nil, nil
	}
	query, args, err := r.sb.Select(planColumns...).
		From("plans").
		Where(sq.Eq{"agent_id": agentIDs, "status": invocation.PlanStatusApproved}).
		OrderBy("submitted_at DESC").
		ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []invocation.Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListRevisions returns plans that declare parentID as their parent, oldest first.
func (r *PlansRepo) ListRevisions(ctx context.Context, parentID string) ([]invocation.Plan, error) {
	query, args, err := r.sb.Select(planColumns...).
		From("plans").
		Where(sq.Eq{"parent_plan_id": parentID}).
		OrderBy("submitted_at ASC").
		ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []invocation.Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanPlan(scanner interface{ Scan(dest ...any) error }) (invocation.Plan, error) {
	var p invocation.Plan
	var actionsJSON string
	var approvalJSON, matchedRuleID, parentPlanID, clientName, clientVersion sql.NullString
	var expiresAt, decidedAt sql.NullTime
	if err := scanner.Scan(
		&p.PlanID, &p.AgentID, &p.Source, &p.ThreadID, &p.Goal, &p.Rationale,
		&actionsJSON, &p.Status, &approvalJSON, &matchedRuleID, &p.Feedback,
		&parentPlanID, &p.Revision, &p.TTLSeconds, &clientName, &clientVersion,
		&expiresAt, &p.SubmittedAt, &decidedAt,
	); err != nil {
		return invocation.Plan{}, err
	}
	if err := json.Unmarshal([]byte(actionsJSON), &p.Actions); err != nil {
		p.Actions = []invocation.PlanAction{}
	}
	if p.Actions == nil {
		p.Actions = []invocation.PlanAction{}
	}
	if approvalJSON.Valid {
		var ap invocation.Approval
		if err := json.Unmarshal([]byte(approvalJSON.String), &ap); err == nil {
			p.Approval = &ap
		}
	}
	if matchedRuleID.Valid {
		p.MatchedRuleID = &matchedRuleID.String
	}
	if parentPlanID.Valid {
		p.ParentPlanID = &parentPlanID.String
	}
	if clientName.Valid {
		p.ClientName = &clientName.String
	}
	if clientVersion.Valid {
		p.ClientVersion = &clientVersion.String
	}
	if expiresAt.Valid {
		t := expiresAt.Time
		p.ExpiresAt = &t
	}
	if decidedAt.Valid {
		t := decidedAt.Time
		p.DecidedAt = &t
	}
	return p, nil
}

func encodePlanJSON(p invocation.Plan) (actionsJSON string, approvalJSON any, err error) {
	actions := p.Actions
	if actions == nil {
		actions = []invocation.PlanAction{}
	}
	b, err := json.Marshal(actions)
	if err != nil {
		return "", nil, err
	}
	if p.Approval != nil {
		ab, err := json.Marshal(p.Approval)
		if err != nil {
			return "", nil, err
		}
		approvalJSON = string(ab)
	}
	return string(b), approvalJSON, nil
}

// PlanEventsRepo persists the append-only plan lifecycle event log.
type PlanEventsRepo struct {
	db *sql.DB
	sb sq.StatementBuilderType
}

func NewPlanEventsRepo(db *sql.DB) *PlanEventsRepo {
	return NewPlanEventsRepoWithDialect(db, DialectSQLite)
}

func NewPlanEventsRepoWithDialect(db *sql.DB, dialect Dialect) *PlanEventsRepo {
	return &PlanEventsRepo{db: db, sb: statementBuilderForDialect(dialect)}
}

func (r *PlanEventsRepo) Create(ctx context.Context, evt invocation.PlanEvent) error {
	query, args, err := r.sb.Insert("plan_events").
		Columns("plan_id", "event_type", "payload_json", "created_at").
		Values(evt.PlanID, evt.EventType, string(evt.Payload), evt.CreatedAt).
		ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *PlanEventsRepo) ListByPlan(ctx context.Context, planID string, filter invocation.EventListFilter) ([]invocation.PlanEvent, int, error) {
	if filter.Limit == 0 {
		filter.Limit = 200
	}
	builder := r.sb.Select("id", "plan_id", "event_type", "payload_json", "created_at").
		From("plan_events").
		Where(sq.Eq{"plan_id": planID})
	query, args, err := builder.OrderBy("id ASC").Limit(filter.Limit).Offset(filter.Offset).ToSql()
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []invocation.PlanEvent
	for rows.Next() {
		var evt invocation.PlanEvent
		var payload string
		if err := rows.Scan(&evt.ID, &evt.PlanID, &evt.EventType, &payload, &evt.CreatedAt); err != nil {
			return nil, 0, err
		}
		evt.Payload = []byte(payload)
		out = append(out, evt)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	countBuilder := r.sb.Select("COUNT(*)").From("plan_events").Where(sq.Eq{"plan_id": planID})
	total, err := countRows(ctx, r.db, countBuilder)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
