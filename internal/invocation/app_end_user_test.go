package invocation_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/store"
)

func appCtx() context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{AgentID: "app-bank", Method: auth.IdentityMethodAPIKey, Capabilities: []string{auth.CapabilityApp}})
}

func endUser(id, tenant string) *invocation.AssertedIdentity {
	o := &invocation.OnBehalfOf{AppAgent: "money-assistant", Token: "tok-" + id,
		EndUser: &invocation.EndUser{ID: id, Email: id + "@example.com", Roles: []string{"retail_customer"}, Tenant: &tenant}}
	return o.Asserted("app-bank")
}

func submitFor(t *testing.T, svc *invocation.Service, a *invocation.AssertedIdentity, key string) invocation.InvocationResponse {
	t.Helper()
	req := invocation.ExternalSubmitRequest{Source: "payments", Tool: "transfer", Input: map[string]any{"amount": 750}, Asserted: a, ClientSessionID: "conv-1"}
	if key != "" {
		req.IdempotencyKey = &key
	}
	resp, err := svc.Submit(appCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// One application's end users share its credential but never its
// idempotency keys: the same key from two end users is two calls, and the
// same end user's redelivery is one.
func TestEndUserIdempotencyIsolation(t *testing.T) {
	svc := newHookedService(t, newSQLiteTestDB(t), "http://unused")
	svc.SetDecider(verdictDecider(invocation.VerdictAllow))
	u1a := submitFor(t, svc, endUser("u_1", "t-a"), "turn-1")
	u1b := submitFor(t, svc, endUser("u_1", "t-a"), "turn-1")
	u2 := submitFor(t, svc, endUser("u_2", "t-a"), "turn-1")
	sameIDOtherTenant := submitFor(t, svc, endUser("u_1", "t-b"), "turn-1")
	if u1a.InvocationID != u1b.InvocationID {
		t.Fatalf("redelivery made a second call: %s %s", u1a.InvocationID, u1b.InvocationID)
	}
	if u2.InvocationID == u1a.InvocationID || sameIDOtherTenant.InvocationID == u1a.InvocationID {
		t.Fatal("another end user's call returned u_1's invocation")
	}
}

// The decider sees the end user (token included); the row keeps their
// details except the token; the answer carries the decider's code, review
// and route and echoes only the end user's id.
func TestEndUserReachesDeciderAndRow(t *testing.T) {
	db := newSQLiteTestDB(t)
	svc := newHookedService(t, db, "http://unused")
	var seen invocation.ToolCall
	var deadline time.Time
	svc.SetDecider(func(ctx context.Context, c invocation.ToolCall) (invocation.Decision, error) {
		seen = c
		deadline, _ = invocation.DecisionDeadline(ctx)
		return invocation.Decision{Verdict: invocation.VerdictPending, Reason: "needs you", ReviewID: "rev_9", Code: "review_opened", Route: "end_user"}, nil
	})
	start := time.Now()
	resp := submitFor(t, svc, endUser("u_1", "t-a"), "")
	if seen.Asserted == nil || seen.Asserted.Token != "tok-u_1" || seen.Asserted.UserID != "u_1" || seen.Asserted.AppAgent != "money-assistant" {
		t.Fatalf("decider saw %+v", seen.Asserted)
	}
	if d := deadline.Sub(start); d < 2*time.Second || d > 4*time.Second {
		t.Fatalf("decision deadline %v from now, want the 3s default", d)
	}
	if resp.Status != invocation.StatusPendingApproval || resp.Approval == nil || resp.Approval.Code != "review_opened" || resp.Approval.ReviewID != "rev_9" || resp.Approval.Route != "end_user" {
		t.Fatalf("response %+v", resp.Approval)
	}
	if resp.UserID == nil || *resp.UserID != "u_1" || resp.Asserted == nil || resp.Asserted.Email != "" || len(resp.Asserted.Roles) != 0 || resp.Asserted.EUTenant != "" {
		t.Fatalf("response identity %+v", resp.Asserted)
	}
	inv, err := store.NewInvocationRepo(db).Get(context.Background(), resp.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	a := inv.Asserted
	if a == nil || a.Email != "u_1@example.com" || len(a.Roles) != 1 || a.EUTenant != "t-a" || !a.HasEUTenant || a.Kind != "app" || a.Token != "" || a.UserKey == "" {
		t.Fatalf("stored %+v", a)
	}
	var raw string
	if err := db.QueryRow(`SELECT COALESCE(request_json,'') || (SELECT group_concat(payload_json) FROM invocation_events WHERE invocation_id = ?) FROM invocations WHERE invocation_id = ?`, resp.InvocationID, resp.InvocationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "tok-u_1") {
		t.Fatal("the end user's token was stored")
	}
}

// Client session ids are scoped per end user: two end users sending the
// same conversation id get different sessions.
func TestEndUserSessionsAreSeparate(t *testing.T) {
	db := newSQLiteTestDB(t)
	svc := newHookedService(t, db, "http://unused")
	svc.SetSessionStore(store.NewExternalSessionRepo(db))
	var sessions []string
	svc.SetDecider(func(_ context.Context, c invocation.ToolCall) (invocation.Decision, error) {
		sessions = append(sessions, c.SessionID)
		return invocation.Decision{Verdict: invocation.VerdictAllow}, nil
	})
	submitFor(t, svc, endUser("u_1", "t-a"), "")
	submitFor(t, svc, endUser("u_2", "t-a"), "")
	submitFor(t, svc, endUser("u_1", "t-a"), "")
	if len(sessions) != 3 || sessions[0] == "" || sessions[0] == sessions[1] || sessions[0] != sessions[2] {
		t.Fatalf("sessions %v: want u_1's two calls in one session, u_2 in another", sessions)
	}
}

func TestRedactAssertedIdentity(t *testing.T) {
	db := newSQLiteTestDB(t)
	svc := newHookedService(t, db, "http://unused")
	svc.SetDecider(verdictDecider(invocation.VerdictAllow))
	u1 := submitFor(t, svc, endUser("u_1", "t-a"), "")
	u2 := submitFor(t, svc, endUser("u_2", "t-a"), "")
	key := invocation.EndUserKey("app-bank", "t-a", true, "u_1")
	n, err := store.RedactAssertedIdentity(context.Background(), db, store.DialectSQLite, "app-bank", key)
	if err != nil || n != 1 {
		t.Fatalf("redacted %d, %v", n, err)
	}
	repo := store.NewInvocationRepo(db)
	got, _ := repo.Get(context.Background(), u1.InvocationID)
	if got.Asserted == nil || got.Asserted.UserID != "" || got.Asserted.Email != "" || len(got.Asserted.Roles) != 0 || got.Asserted.UserKey != "" || got.Tool != "transfer" {
		t.Fatalf("after redaction %+v", got.Asserted)
	}
	if leaked := countLike(t, db, u1.InvocationID, "u_1"); leaked != 0 {
		t.Fatalf("%d event payloads still name u_1", leaked)
	}
	other, _ := repo.Get(context.Background(), u2.InvocationID)
	if other.Asserted == nil || other.Asserted.UserID != "u_2" || countLike(t, db, u2.InvocationID, "u_2") == 0 {
		t.Fatal("another end user's rows were touched")
	}
}

func countLike(t *testing.T, db *sql.DB, invocationID, s string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM invocation_events WHERE invocation_id = ? AND payload_json LIKE ?`, invocationID, "%"+s+"%").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
