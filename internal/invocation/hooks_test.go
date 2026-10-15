package invocation_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/config"
	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/invocation/policy"
	"github.com/validmind/atryum/internal/mcp"
	"github.com/validmind/atryum/internal/store"
)

// fakeMCP is an upstream MCP server that counts tools/call requests.
func fakeMCP(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body["method"] {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": body["id"], "result": map[string]any{"serverInfo": map[string]any{"name": "fake", "version": "0"}, "capabilities": map[string]any{}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			calls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": body["id"], "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// newHookedService builds a service on db with transactions wired as
// pkg/atryum wires them. The default policy approves everything, so a test
// that sees a decider's deny knows the decider, not the policy, decided.
func newHookedService(t *testing.T, db *sql.DB, upstreamURL string) *invocation.Service {
	t.Helper()
	resolver := mcp.NewResolver(store.NewServerRepo(db), config.Config{
		Upstreams: []config.UpstreamConfig{{Name: "bank", Mode: "http", BaseURL: upstreamURL, Enabled: true, TimeoutSeconds: 5}},
	})
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc := invocation.NewService(store.NewInvocationRepo(db), store.NewEventRepo(db), resolver, mcp.NewHTTPClient(), policy.AlwaysApproveProvider{}, 5*time.Second, nil, nil, nil, nil)
	svc.SetTx(func(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
		return store.InTx(ctx, db, fn)
	})
	return svc
}

func verdictDecider(v invocation.Verdict) invocation.Decider {
	return func(context.Context, invocation.ToolCall) (invocation.Decision, error) {
		return invocation.Decision{Verdict: v, Reason: "test", ReviewID: "rev_1"}, nil
	}
}

func TestDeciderOnProxyPath(t *testing.T) {
	for _, tc := range []struct {
		name      string
		decider   invocation.Decider
		want      invocation.Status
		wantCalls int32
	}{
		{"allow", verdictDecider(invocation.VerdictAllow), invocation.StatusSucceeded, 1},
		{"notify", verdictDecider(invocation.VerdictNotify), invocation.StatusSucceeded, 1},
		{"deny", verdictDecider(invocation.VerdictDeny), invocation.StatusDenied, 0},
		{"error fails closed", func(context.Context, invocation.ToolCall) (invocation.Decision, error) {
			return invocation.Decision{}, errors.New("boom")
		}, invocation.StatusDenied, 0},
		{"unknown verdict fails closed", verdictDecider("maybe"), invocation.StatusDenied, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream, calls := fakeMCP(t)
			svc := newHookedService(t, newSQLiteTestDB(t), upstream.URL)
			var seen invocation.ToolCall
			svc.SetDecider(func(ctx context.Context, c invocation.ToolCall) (invocation.Decision, error) {
				seen = c
				return tc.decider(ctx, c)
			})
			resp, err := svc.Invoke(context.Background(), invocation.CreateInvocationRequest{Server: "bank", Tool: "pay", Input: map[string]any{"amount": 5}})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Status != tc.want || calls.Load() != tc.wantCalls {
				t.Fatalf("status=%s calls=%d, want %s and %d", resp.Status, calls.Load(), tc.want, tc.wantCalls)
			}
			if seen.Path != "proxy" || seen.Server != "bank" || seen.Tool != "pay" || string(seen.Arguments) != `{"amount":5}` || seen.InvocationID != resp.InvocationID {
				t.Fatalf("decider saw %+v", seen)
			}
		})
	}
}

func TestDeciderOnSubmitPath(t *testing.T) {
	for _, tc := range []struct {
		verdict invocation.Verdict
		want    invocation.Status
	}{
		{invocation.VerdictAllow, invocation.StatusApproved},
		{invocation.VerdictNotify, invocation.StatusApproved},
		{invocation.VerdictDeny, invocation.StatusDenied},
		{invocation.VerdictPending, invocation.StatusPendingApproval},
	} {
		t.Run(string(tc.verdict), func(t *testing.T) {
			svc := newHookedService(t, newSQLiteTestDB(t), "http://unused")
			svc.SetDecider(verdictDecider(tc.verdict))
			resp, err := svc.Submit(context.Background(), invocation.ExternalSubmitRequest{Source: "claude-code", Tool: "Bash", Input: map[string]any{"cmd": "ls"}})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Status != tc.want {
				t.Fatalf("status=%s, want %s", resp.Status, tc.want)
			}
			evts, err := svc.Events(context.Background(), resp.InvocationID, invocation.EventListFilter{})
			if err != nil {
				t.Fatal(err)
			}
			var got struct{ Decision invocation.Decision }
			_ = json.Unmarshal(evts.Items[0].Data, &got)
			if evts.Items[0].Type != "invocation.received" || got.Decision.Verdict != tc.verdict {
				t.Fatalf("first event %s %s, want invocation.received with verdict %s", evts.Items[0].Type, evts.Items[0].Data, tc.verdict)
			}
		})
	}
}

// A pending call is held by polling its row, so a decision recorded by a
// different service instance on the same database (another replica) releases it.
func TestPendingCallReleasedByAnotherInstance(t *testing.T) {
	for _, tc := range []struct {
		name      string
		approve   bool
		want      invocation.Status
		wantCalls int32
	}{
		{"approved", true, invocation.StatusSucceeded, 1},
		{"denied", false, invocation.StatusDenied, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream, calls := fakeMCP(t)
			db := newSQLiteTestDB(t)
			holder := newHookedService(t, db, upstream.URL)
			holder.SetDecider(verdictDecider(invocation.VerdictPending))
			other := newHookedService(t, db, upstream.URL)

			go func() {
				for {
					time.Sleep(20 * time.Millisecond)
					list, err := other.List(context.Background(), invocation.InvocationListFilter{Limit: 1})
					if err != nil || len(list.Items) == 0 || list.Items[0].Status != invocation.StatusPendingApproval {
						continue
					}
					id := list.Items[0].InvocationID
					if tc.approve {
						err = other.Approve(context.Background(), id, "manager@bank")
					} else {
						err = other.Deny(context.Background(), id, "no", "manager@bank")
					}
					if err != nil {
						t.Error(err)
					}
					return
				}
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, err := holder.Invoke(ctx, invocation.CreateInvocationRequest{Server: "bank", Tool: "pay", Input: map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Status != tc.want || calls.Load() != tc.wantCalls {
				t.Fatalf("status=%s calls=%d, want %s and %d", resp.Status, calls.Load(), tc.want, tc.wantCalls)
			}
			// A second decision on a decided call is refused.
			if err := other.Approve(context.Background(), resp.InvocationID, "x"); err == nil {
				t.Fatal("second decision was accepted")
			}
		})
	}
}

// The hook runs in the transaction that writes each change: what it writes
// with the tx commits with the change, and its error rolls the change back.
func TestEventHookRunsInTheChangeTransaction(t *testing.T) {
	upstream, calls := fakeMCP(t)
	db := newSQLiteTestDB(t)
	if _, err := db.Exec(`CREATE TABLE hook_log (type TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	svc := newHookedService(t, db, upstream.URL)
	svc.SetDecider(verdictDecider(invocation.VerdictAllow))
	svc.SetEventHook(func(ctx context.Context, tx *sql.Tx, c invocation.Change) error {
		if tx == nil {
			return errors.New("no transaction")
		}
		if c.Invocation.Tool != "pay" {
			return errors.New("change without its invocation")
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO hook_log (type) VALUES (?)`, c.Type)
		return err
	})
	if _, err := svc.Invoke(context.Background(), invocation.CreateInvocationRequest{Server: "bank", Tool: "pay", Input: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if got := hookLog(t, db); got != "invocation.created,invocation.received,invocation.executing,invocation.succeeded" {
		t.Fatalf("hook log = %s", got)
	}

	// A hook failure on the way to execution: the call fails, the tool never
	// runs, and neither the status change nor its event is written.
	if _, err := db.Exec(`DELETE FROM hook_log`); err != nil {
		t.Fatal(err)
	}
	calls.Store(0)
	svc.SetEventHook(func(ctx context.Context, tx *sql.Tx, c invocation.Change) error {
		if c.Type == "invocation.executing" {
			return errors.New("record unavailable")
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO hook_log (type) VALUES (?)`, c.Type)
		return err
	})
	if _, err := svc.Invoke(context.Background(), invocation.CreateInvocationRequest{Server: "bank", Tool: "pay", Input: map[string]any{}}); err == nil {
		t.Fatal("Invoke succeeded although the hook failed")
	}
	if calls.Load() != 0 {
		t.Fatal("tool ran although the hook failed")
	}
	var executing int
	if err := db.QueryRow(`SELECT COUNT(*) FROM invocations WHERE status = 'executing'`).Scan(&executing); err != nil || executing != 0 {
		t.Fatalf("executing rows = %d (%v), want 0", executing, err)
	}
	if got := hookLog(t, db); got != "invocation.created,invocation.received" {
		t.Fatalf("hook log = %s", got)
	}
}

func hookLog(t *testing.T, db *sql.DB) string {
	t.Helper()
	var got sql.NullString
	if err := db.QueryRow(`SELECT group_concat(type) FROM hook_log`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got.String
}

// failingHook fails on the listed change types and records the rest.
func failingHook(fail ...string) (invocation.EventHook, *[]string) {
	var seen []string
	return func(_ context.Context, _ *sql.Tx, c invocation.Change) error {
		for _, f := range fail {
			if c.Type == f {
				return errors.New("record unavailable")
			}
		}
		seen = append(seen, c.Type)
		return nil
	}, &seen
}

// A decision that cannot be recorded never runs: the call fails closed, even
// when the denial cannot be recorded either.
func TestUnrecordedDecisionFailsClosed(t *testing.T) {
	for _, fail := range [][]string{{"invocation.received"}, {"invocation.received", "invocation.denied"}} {
		upstream, calls := fakeMCP(t)
		svc := newHookedService(t, newSQLiteTestDB(t), upstream.URL)
		svc.SetDecider(verdictDecider(invocation.VerdictAllow))
		hook, _ := failingHook(fail...)
		svc.SetEventHook(hook)
		resp, err := svc.Invoke(context.Background(), invocation.CreateInvocationRequest{Server: "bank", Tool: "pay", Input: map[string]any{}})
		if err != nil || resp.Status != invocation.StatusDenied || calls.Load() != 0 {
			t.Fatalf("failing %v: status=%s err=%v calls=%d; want denied and no call", fail, resp.Status, err, calls.Load())
		}
	}
}

// Once the tool has run, a record failure is not an error (which would invite
// a retry), and a caller that hangs up mid-call does not stop the outcome
// from being recorded.
func TestOutcomeAfterExecution(t *testing.T) {
	t.Run("record failure is not an error", func(t *testing.T) {
		upstream, calls := fakeMCP(t)
		svc := newHookedService(t, newSQLiteTestDB(t), upstream.URL)
		svc.SetDecider(verdictDecider(invocation.VerdictAllow))
		hook, _ := failingHook("invocation.succeeded")
		svc.SetEventHook(hook)
		resp, err := svc.Invoke(context.Background(), invocation.CreateInvocationRequest{Server: "bank", Tool: "pay", Input: map[string]any{}})
		if err != nil || resp.Status != invocation.StatusSucceeded || calls.Load() != 1 {
			t.Fatalf("status=%s err=%v calls=%d; want succeeded, no error, one call", resp.Status, err, calls.Load())
		}
	})
	t.Run("caller hangs up during the call", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["method"] == "tools/call" {
				cancel() // the tool ran; now the caller goes away
				time.Sleep(50 * time.Millisecond)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": body["id"], "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}})
		}))
		defer upstream.Close()
		db := newSQLiteTestDB(t)
		svc := newHookedService(t, db, upstream.URL)
		svc.SetDecider(verdictDecider(invocation.VerdictAllow))
		hook, seen := failingHook()
		svc.SetEventHook(hook)
		resp, err := svc.Invoke(ctx, invocation.CreateInvocationRequest{Server: "bank", Tool: "pay", Input: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		got, _ := svc.Get(context.Background(), resp.InvocationID)
		last := (*seen)[len(*seen)-1]
		if got.Status == invocation.StatusExecuting || (last != "invocation.failed" && last != "invocation.succeeded") {
			t.Fatalf("row %s, last change %s: the outcome was not recorded", got.Status, last)
		}
	})
}

// A caller that goes away after its call was approved leaves the row failed
// with the recorded approval intact.
func TestCancelAfterApprovalKeepsTheApproval(t *testing.T) {
	upstream, calls := fakeMCP(t)
	db := newSQLiteTestDB(t)
	holder := newHookedService(t, db, upstream.URL)
	holder.SetDecider(verdictDecider(invocation.VerdictPending))
	invocation.SetApprovalPoll(holder, time.Hour) // the holder never sees the approval
	other := newHookedService(t, db, upstream.URL)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			time.Sleep(20 * time.Millisecond)
			list, err := other.List(context.Background(), invocation.InvocationListFilter{Limit: 1})
			if err == nil && len(list.Items) == 1 && list.Items[0].Status == invocation.StatusPendingApproval {
				if err := other.Approve(context.Background(), list.Items[0].InvocationID, "manager@bank"); err != nil {
					t.Error(err)
				}
				cancel()
				return
			}
		}
	}()
	resp, err := holder.Invoke(ctx, invocation.CreateInvocationRequest{Server: "bank", Tool: "pay", Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := other.Get(context.Background(), resp.InvocationID)
	if got.Status != invocation.StatusFailed || calls.Load() != 0 {
		t.Fatalf("status=%s calls=%d; want failed and not run", got.Status, calls.Load())
	}
	if got.Approval == nil || got.Approval.Status != "approved" || got.Approval.ActorID == nil || *got.Approval.ActorID != "manager@bank" {
		t.Fatalf("approval = %+v; want the recorded approval by manager@bank", got.Approval)
	}
}

func TestDeciderPanicDenies(t *testing.T) {
	upstream, calls := fakeMCP(t)
	svc := newHookedService(t, newSQLiteTestDB(t), upstream.URL)
	svc.SetDecider(func(context.Context, invocation.ToolCall) (invocation.Decision, error) { panic("boom") })
	resp, err := svc.Invoke(context.Background(), invocation.CreateInvocationRequest{Server: "bank", Tool: "pay", Input: map[string]any{}})
	if err != nil || resp.Status != invocation.StatusDenied || calls.Load() != 0 {
		t.Fatalf("status=%s err=%v calls=%d; want denied", resp.Status, err, calls.Load())
	}
	sub, err := svc.Submit(context.Background(), invocation.ExternalSubmitRequest{Source: "hook", Tool: "Bash", Input: map[string]any{}})
	if err != nil || sub.Status != invocation.StatusDenied {
		t.Fatalf("submit status=%s err=%v; want denied", sub.Status, err)
	}
}

// Under a decider a held MCP proxy call's arguments are not sent to Atryum's
// summarizer; without one (a manual-approval policy holds it) they are.
func TestNoSummaryUnderADecider(t *testing.T) {
	for _, withDecider := range []bool{false, true} {
		upstream, _ := fakeMCP(t)
		db := newSQLiteTestDB(t)
		resolver := mcp.NewResolver(store.NewServerRepo(db), config.Config{
			Upstreams: []config.UpstreamConfig{{Name: "bank", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
		})
		if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
			t.Fatal(err)
		}
		svc := invocation.NewService(store.NewInvocationRepo(db), store.NewEventRepo(db), resolver, mcp.NewHTTPClient(), policy.ManualApprovalProvider{}, 5*time.Second, nil, nil, nil, summarySettingsStub{modelConfigCUID: "model"})
		summarizer := &summaryClientStub{summary: "s"}
		svc.SetInvocationSummarizer(summarizer)
		if withDecider {
			svc.SetDecider(verdictDecider(invocation.VerdictPending))
		}
		// The call is held until the caller gives up.
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		resp, err := svc.Invoke(ctx, invocation.CreateInvocationRequest{Server: "bank", Tool: "pay", Input: map[string]any{"secret": "x"}})
		cancel()
		if err != nil || resp.Status != invocation.StatusFailed {
			t.Fatalf("decider=%v: status=%s err=%v; want the held call to fail when the caller leaves", withDecider, resp.Status, err)
		}
		time.Sleep(100 * time.Millisecond)
		if called := summarizer.request().Invocation != nil; called == withDecider {
			t.Fatalf("decider=%v: summarizer called=%v", withDecider, called)
		}
	}
}

// A verified identity wins over an agent_id in the request body.
func TestSubmitUsesTheVerifiedIdentity(t *testing.T) {
	svc := newHookedService(t, newSQLiteTestDB(t), "http://unused")
	svc.SetDecider(verdictDecider(invocation.VerdictAllow))
	ctx := auth.WithIdentity(context.Background(), auth.Identity{AgentID: "agent-a", Issuer: "https://idp", Subject: "agent-a"})
	resp, err := svc.Submit(ctx, invocation.ExternalSubmitRequest{Source: "hook", Tool: "Bash", AgentID: "agent-b", Input: map[string]any{}})
	if err != nil || resp.AgentID == nil || *resp.AgentID != "agent-a" {
		t.Fatalf("agent = %v (%v), want agent-a", resp.AgentID, err)
	}
}

// A stuck report is recorded through the hook and authorizes nothing: the
// decider is never asked, no upstream is called, and the hook sees one
// distinct kind, never invocation.created.
func TestReportStuck(t *testing.T) {
	upstream, calls := fakeMCP(t)
	db := newSQLiteTestDB(t)
	svc := newHookedService(t, db, upstream.URL)
	svc.SetDecider(func(context.Context, invocation.ToolCall) (invocation.Decision, error) {
		t.Error("decider was asked about a stuck report")
		return invocation.Decision{Verdict: invocation.VerdictDeny}, nil
	})
	var types []string
	var seen invocation.Change
	svc.SetEventHook(func(_ context.Context, _ *sql.Tx, c invocation.Change) error {
		types = append(types, c.Type)
		seen = c
		return nil
	})

	for _, bad := range []struct {
		server string
		args   map[string]any
	}{
		{"bank", map[string]any{"task": "pay INV-1"}},
		{"bank", map[string]any{"task": " ", "boundary": "x"}},
		{"nope", map[string]any{"task": "pay INV-1", "boundary": "P-02"}},
	} {
		if _, err := svc.ReportStuck(context.Background(), bad.server, bad.args); !errors.Is(err, invocation.ErrInvalidStuckReport) {
			t.Fatalf("ReportStuck(%s, %v) = %v, want ErrInvalidStuckReport", bad.server, bad.args, err)
		}
	}
	if len(types) != 0 {
		t.Fatalf("rejected reports reached the hook: %v", types)
	}

	resp, err := svc.ReportStuck(context.Background(), "bank", map[string]any{"task": "pay INV-1", "boundary": "P-02 needs a person", "detail": strings.Repeat("é", 3000), "extra": "dropped"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(types, ","); got != "invocation.stuck_reported" {
		t.Fatalf("hook saw %s", got)
	}
	if seen.Invocation.Tool != invocation.StuckReportTool || seen.Invocation.Upstream != "bank" || seen.Invocation.Status != invocation.StatusReported {
		t.Fatalf("hook change %+v", seen.Invocation)
	}
	var stored struct{ Arguments map[string]string }
	if err := json.Unmarshal(seen.Payload, &stored); err != nil {
		t.Fatal(err)
	}
	if d := stored.Arguments["detail"]; len(d) > 4096+len("…") || !strings.HasSuffix(d, "…") || !utf8.ValidString(d) {
		t.Fatalf("detail not capped cleanly: %d bytes", len(d))
	}
	if _, ok := stored.Arguments["extra"]; ok || stored.Arguments["boundary"] != "P-02 needs a person" {
		t.Fatalf("stored arguments %v", stored.Arguments)
	}
	if resp.Status != invocation.StatusReported || !strings.Contains(string(resp.Result), "Stuck report recorded") || calls.Load() != 0 {
		t.Fatalf("status=%s result=%s calls=%d", resp.Status, resp.Result, calls.Load())
	}
	if got, err := svc.Get(context.Background(), resp.InvocationID); err != nil || got.Status != invocation.StatusReported || !strings.Contains(string(got.Result), "Stuck report recorded") {
		t.Fatalf("stored row %+v err=%v", got, err)
	}
}

// ResolvePending joins the embedder's transaction: rolled back, the call stays
// held; committed, it is released; resolved again, it reports ErrNotPending.
func TestResolvePendingInEmbedderTransaction(t *testing.T) {
	upstream, calls := fakeMCP(t)
	db := newSQLiteTestDB(t)
	svc := newHookedService(t, db, upstream.URL)
	svc.SetDecider(verdictDecider(invocation.VerdictPending))
	invocation.SetApprovalPoll(svc, 10*time.Millisecond)
	if _, err := db.Exec(`CREATE TABLE review_log (decision TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	svc.SetEventHook(func(ctx context.Context, tx *sql.Tx, c invocation.Change) error {
		if c.Type == "invocation.approved" {
			_, err := tx.ExecContext(ctx, `INSERT INTO review_log (decision) VALUES ('approved')`)
			return err
		}
		return nil
	})

	done := make(chan invocation.InvocationResponse, 1)
	go func() {
		resp, err := svc.Invoke(context.Background(), invocation.CreateInvocationRequest{Server: "bank", Tool: "pay", Input: map[string]any{}})
		if err != nil {
			t.Error(err)
		}
		done <- resp
	}()
	var id string
	for id == "" {
		time.Sleep(10 * time.Millisecond)
		list, err := svc.List(context.Background(), invocation.InvocationListFilter{Limit: 1})
		if err == nil && len(list.Items) == 1 && list.Items[0].Status == invocation.StatusPendingApproval {
			id = list.Items[0].InvocationID
		}
	}

	resolve := func(commit bool) {
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.ResolvePending(store.WithTx(context.Background(), tx), id, true, "manager@bank", "ok"); err != nil {
			t.Fatal(err)
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	resolve(false)
	time.Sleep(50 * time.Millisecond)
	if got, _ := svc.Get(context.Background(), id); got.Status != invocation.StatusPendingApproval {
		t.Fatalf("after rollback status=%s, want pending_approval", got.Status)
	}
	resolve(true)
	if resp := <-done; resp.Status != invocation.StatusSucceeded || calls.Load() != 1 {
		t.Fatalf("status=%s calls=%d, want succeeded and 1", resp.Status, calls.Load())
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM review_log`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("review_log rows=%d err=%v, want 1 (the rolled-back write must not stick)", n, err)
	}
	if err := svc.ResolvePending(context.Background(), id, false, "x", "late"); !errors.Is(err, invocation.ErrNotPending) {
		t.Fatalf("second resolve: %v, want ErrNotPending", err)
	}
}

// fakeWaker never announces anything; it only reports its connection state.
type fakeWaker struct{ connected bool }

func (fakeWaker) Wait(string) (<-chan struct{}, func()) { return nil, func() {} }
func (w fakeWaker) Connected() bool                     { return w.connected }

// A held call polls at the slow pace only while its waker is connected.
func TestHeldCallPollPaceFollowsWaker(t *testing.T) {
	for _, tc := range []struct {
		connected bool
		released  bool
	}{{false, true}, {true, false}} {
		upstream, _ := fakeMCP(t)
		db := newSQLiteTestDB(t)
		holder := newHookedService(t, db, upstream.URL)
		holder.SetDecider(verdictDecider(invocation.VerdictPending))
		holder.SetDecisionWaker(fakeWaker{tc.connected})
		invocation.SetPolls(holder, 20*time.Millisecond, time.Hour)
		other := newHookedService(t, db, upstream.URL)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		go func() {
			for {
				time.Sleep(20 * time.Millisecond)
				list, err := other.List(context.Background(), invocation.InvocationListFilter{Limit: 1})
				if err == nil && len(list.Items) == 1 && list.Items[0].Status == invocation.StatusPendingApproval {
					_ = other.Approve(context.Background(), list.Items[0].InvocationID, "x")
					return
				}
			}
		}()
		resp, err := holder.Invoke(ctx, invocation.CreateInvocationRequest{Server: "bank", Tool: "pay", Input: map[string]any{}})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if released := resp.Status == invocation.StatusSucceeded; released != tc.released {
			t.Fatalf("connected=%v: status=%s, want released=%v", tc.connected, resp.Status, tc.released)
		}
	}
}
