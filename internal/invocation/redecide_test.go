package invocation_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/config"
	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/invocation/policy"
	"github.com/validmind/atryum/internal/mcp"
	"github.com/validmind/atryum/internal/store"
)

// replicaSet builds service instances ("replicas") sharing one database, as
// replicas of an embedding program share one Postgres. Each replica has its
// own pool on Postgres; on SQLite they share the test's single connection.
type replicaSet struct {
	t       *testing.T
	dialect store.Dialect
	open    func() *sql.DB
	db      *sql.DB // the first pool, for checks
}

func sqliteReplicas(t *testing.T) *replicaSet {
	db := newSQLiteTestDB(t)
	return &replicaSet{t: t, dialect: store.DialectSQLite, open: func() *sql.DB { return db }, db: db}
}

func pgReplicas(t *testing.T) *replicaSet {
	upstream, _ := fakeMCP(t)
	dsn, _ := postgresReplicas(t, upstream.URL)
	rs := &replicaSet{t: t, dialect: store.DialectPostgres}
	rs.open = func() *sql.DB {
		db, err := store.OpenPostgres(dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err := store.InitDBWithDialect(db, store.DialectPostgres); err != nil {
			t.Fatal(err)
		}
		return db
	}
	rs.db = rs.open()
	return rs
}

// recorded counts the changes a replica's event hook saw committed.
type recorded struct {
	mu    sync.Mutex
	kinds map[string]int
}

func (r *recorded) count(kind string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.kinds[kind]
}

// replica is a service on rs's database with rules, decider d (nil for
// rules and the default policy) and an event hook that tallies into rec
// once its transaction commits.
func (rs *replicaSet) replica(rules []invocation.ApprovalRule, d invocation.Decider, rec *recorded) *invocation.Service {
	db := rs.db
	if rs.dialect == store.DialectPostgres {
		db = rs.open()
	}
	resolver := mcp.NewResolver(store.NewServerRepoWithDialect(db, rs.dialect), config.Config{})
	var rulesStore interface {
		ListApprovalRules(context.Context) ([]invocation.ApprovalRule, error)
	}
	if rules != nil {
		rulesStore = rulesStoreStub{rules: rules}
	}
	svc := invocation.NewService(store.NewInvocationRepoWithDialect(db, rs.dialect), store.NewEventRepoWithDialect(db, rs.dialect), resolver, mcp.NewHTTPClient(), policy.AlwaysApproveProvider{}, 5*time.Second, rulesStore, nil, nil, nil)
	svc.SetTx(func(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
		var pending []string
		err := store.InTx(context.WithValue(ctx, pendingKey{}, &pending), db, fn)
		if err == nil && rec != nil {
			rec.mu.Lock()
			for _, k := range pending {
				rec.kinds[k]++
			}
			rec.mu.Unlock()
		}
		return err
	})
	// The hook appends to the pending list of the transaction it runs in;
	// each SetTx call above owns one, reached through the context.
	svc.SetEventHook(func(ctx context.Context, tx *sql.Tx, c invocation.Change) error {
		if p, ok := ctx.Value(pendingKey{}).(*[]string); ok {
			*p = append(*p, c.Type)
		}
		return nil
	})
	if d != nil {
		svc.SetDecider(d)
	}
	return svc
}

type pendingKey struct{}

func newRecorded() *recorded { return &recorded{kinds: map[string]int{}} }

// strandRow saves a call as received with idempotency key, submitted long
// enough ago that nobody is still deciding it, as a replica that died after
// saving it and before its decision committed leaves it.
func (rs *replicaSet) strandRow(agent, key string, age time.Duration) string {
	t := rs.t
	id := fmt.Sprintf("inv_stranded_%d", time.Now().UnixNano())
	k := key
	inv := invocation.Invocation{
		InvocationID:   id,
		IdempotencyKey: &k,
		Tool:           "Bash",
		Status:         invocation.StatusReceived,
		Input:          []byte(`{"command":"make deploy"}`),
		SubmittedAt:    time.Now().UTC().Add(-age),
		AgentID:        &agent,
	}
	if err := store.NewInvocationRepoWithDialect(rs.db, rs.dialect).Create(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	return id
}

func retry(svc *invocation.Service, agent, key string) (invocation.InvocationResponse, error) {
	k := key
	return svc.Submit(as(agent), invocation.ExternalSubmitRequest{Source: "harness", Tool: "Bash", Input: map[string]any{"command": "make deploy"}, IdempotencyKey: &k})
}

// eventKinds counts id's stored events by type.
func (rs *replicaSet) eventKinds(id string) map[string]int {
	evts, _, err := store.NewEventRepoWithDialect(rs.db, rs.dialect).ListByInvocation(context.Background(), id, invocation.EventListFilter{Limit: 100})
	if err != nil {
		rs.t.Fatal(err)
	}
	n := map[string]int{}
	for _, e := range evts {
		n[e.EventType]++
	}
	return n
}

func countingDecider(v invocation.Verdict, calls *atomic.Int32, delay time.Duration) invocation.Decider {
	return func(context.Context, invocation.ToolCall) (invocation.Decision, error) {
		calls.Add(1)
		time.Sleep(delay)
		return invocation.Decision{Verdict: v, Reason: "test"}, nil
	}
}

// A call saved as received whose decision never committed is decided by
// the agent's retry with the same idempotency key, once: a later retry gets
// the recorded decision without asking again.
func TestRetryDecidesStrandedCall(t *testing.T) {
	checkRetryDecidesStrandedCall(t, sqliteReplicas(t))
}

func TestPostgresRetryDecidesStrandedCall(t *testing.T) {
	checkRetryDecidesStrandedCall(t, pgReplicas(t))
}

func checkRetryDecidesStrandedCall(t *testing.T, rs *replicaSet) {
	var calls atomic.Int32
	rec := newRecorded()
	b := rs.replica(nil, countingDecider(invocation.VerdictAllow, &calls, 0), rec)
	id := rs.strandRow("agent-a", "stranded", time.Hour)

	got, err := retry(b, "agent-a", "stranded")
	if err != nil || got.InvocationID != id || got.Status != invocation.StatusApproved {
		t.Fatalf("retry: %s %s %v, want %s approved", got.InvocationID, got.Status, err, id)
	}
	again, err := retry(b, "agent-a", "stranded")
	if err != nil || again.InvocationID != id || again.Status != invocation.StatusApproved {
		t.Fatalf("second retry: %s %s %v, want %s approved", again.InvocationID, again.Status, err, id)
	}
	if calls.Load() != 1 {
		t.Errorf("decider asked %d times, want 1", calls.Load())
	}
	if n := rs.eventKinds(id); n["invocation.received"] != 1 || n["invocation.approved"] != 1 || len(n) != 2 {
		t.Errorf("stored events %v, want one received and one approved", n)
	}
	if rec.count("invocation.received") != 1 || rec.count("invocation.approved") != 1 {
		t.Errorf("hook saw %v, want one received and one approved", rec.kinds)
	}
}

// A call saved moments ago may still be in its first decision on a live
// replica: a retry inside the grace period answers received without
// deciding it a second time.
func TestRetryLeavesFreshReceivedCall(t *testing.T) {
	rs := sqliteReplicas(t)
	var calls atomic.Int32
	b := rs.replica(nil, countingDecider(invocation.VerdictAllow, &calls, 0), nil)
	id := rs.strandRow("agent-a", "fresh", 0)
	got, err := retry(b, "agent-a", "fresh")
	if err != nil || got.InvocationID != id || got.Status != invocation.StatusReceived || calls.Load() != 0 {
		t.Fatalf("retry: %s %s %v calls=%d, want %s received and no decision", got.InvocationID, got.Status, err, calls.Load(), id)
	}
}

// Retries of one stranded call racing on two replicas record exactly one
// decision: one received event and one verdict, whatever each replica's
// decider said, and every retry answers with that decision.
func TestConcurrentRetriesDecideStrandedCallOnce(t *testing.T) {
	checkConcurrentRetries(t, sqliteReplicas(t), nil)
}

func TestPostgresConcurrentRetriesDecideStrandedCallOnce(t *testing.T) {
	checkConcurrentRetries(t, pgReplicas(t), nil)
}

// The same on the rules path: the rule's audit event commits with the
// decision, so a losing replica leaves no rule_evaluated of its own.
func TestConcurrentRetriesDecideStrandedCallOnceByRule(t *testing.T) {
	checkConcurrentRetries(t, sqliteReplicas(t), []invocation.ApprovalRule{{Action: invocation.RuleActionAutoApprove, Enabled: true}})
}

func TestPostgresConcurrentRetriesDecideStrandedCallOnceByRule(t *testing.T) {
	checkConcurrentRetries(t, pgReplicas(t), []invocation.ApprovalRule{{Action: invocation.RuleActionAutoApprove, Enabled: true}})
}

func checkConcurrentRetries(t *testing.T, rs *replicaSet, rules []invocation.ApprovalRule) {
	var calls atomic.Int32
	rec := newRecorded()
	var d invocation.Decider
	if rules == nil {
		d = countingDecider(invocation.VerdictAllow, &calls, 5*time.Millisecond)
	}
	replicas := []*invocation.Service{rs.replica(rules, d, rec), rs.replica(rules, d, rec)}
	id := rs.strandRow("agent-a", "race", time.Hour)

	const n = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	resps := make([]invocation.InvocationResponse, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			resps[i], errs[i] = retry(replicas[i%len(replicas)], "agent-a", "race")
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range resps {
		if errs[i] != nil || resps[i].InvocationID != id || resps[i].Status != invocation.StatusApproved {
			t.Errorf("retry %d: %s %s %v, want %s approved", i, resps[i].InvocationID, resps[i].Status, errs[i], id)
		}
	}
	want := map[string]int{"invocation.received": 1, "invocation.approved": 1}
	if rules != nil {
		want["invocation.rule_evaluated"] = 1
	}
	got := rs.eventKinds(id)
	for k, v := range want {
		if got[k] != v || rec.count(k) != v {
			t.Errorf("%s: %d stored, %d seen by the hook, want %d (stored %v)", k, got[k], rec.count(k), v, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("stored events %v, want %v", got, want)
	}
}

// The replica that saved the call is alive but slow: a retry on another
// replica decides it first, and the first replica's late decision records
// nothing. Both callers get the retry's decision.
func TestSlowFirstDecisionLosesToRetry(t *testing.T) {
	checkSlowFirstDecisionLosesToRetry(t, sqliteReplicas(t))
}

func TestPostgresSlowFirstDecisionLosesToRetry(t *testing.T) {
	checkSlowFirstDecisionLosesToRetry(t, pgReplicas(t))
}

func checkSlowFirstDecisionLosesToRetry(t *testing.T, rs *replicaSet) {
	rec := newRecorded()
	started, release := make(chan struct{}), make(chan struct{})
	a := rs.replica(nil, func(context.Context, invocation.ToolCall) (invocation.Decision, error) {
		close(started)
		<-release
		return invocation.Decision{Verdict: invocation.VerdictAllow, Reason: "slow"}, nil
	}, rec)
	var calls atomic.Int32
	b := rs.replica(nil, countingDecider(invocation.VerdictDeny, &calls, 0), rec)
	invocation.SetRedecideAfter(b, 0)

	first := make(chan invocation.InvocationResponse, 1)
	go func() {
		resp, err := retry(a, "agent-a", "slow")
		if err != nil {
			t.Error(err)
		}
		first <- resp
	}()
	<-started
	got, err := retry(b, "agent-a", "slow")
	if err != nil || got.Status != invocation.StatusDenied || calls.Load() != 1 {
		t.Fatalf("retry on b: %s %v calls=%d, want denied by b's decider", got.Status, err, calls.Load())
	}
	close(release)
	late := <-first
	if late.InvocationID != got.InvocationID || late.Status != invocation.StatusDenied {
		t.Fatalf("a's late answer: %s %s, want %s denied (the recorded decision)", late.InvocationID, late.Status, got.InvocationID)
	}
	if n := rs.eventKinds(got.InvocationID); n["invocation.received"] != 1 || n["invocation.denied"] != 1 || len(n) != 2 {
		t.Errorf("stored events %v, want one received and one denied", n)
	}
	if rec.count("invocation.received") != 1 || rec.count("invocation.denied") != 1 || rec.count("invocation.approved") != 0 {
		t.Errorf("hook saw %v, want one received and one denied", rec.kinds)
	}
}

// Deciding a saved call again, its session history leaves the call itself
// out, as it was left out when the call was first decided, before it was
// saved.
func TestRedecidedCallIsNotItsOwnSessionHistory(t *testing.T) {
	rs := sqliteReplicas(t)
	svc := rs.replica(nil, nil, nil)
	repo := store.NewInvocationRepo(rs.db)
	session, agent := "sess_1", "agent-a"
	for i, tool := range []string{"Read", "Bash"} {
		if err := repo.Create(context.Background(), invocation.Invocation{
			InvocationID: fmt.Sprintf("inv_%d", i), Tool: tool, Status: invocation.StatusReceived,
			Input: []byte(`{}`), SubmittedAt: time.Now().UTC().Add(time.Duration(i) * time.Second), AgentID: &agent, SessionID: &session,
		}); err != nil {
			t.Fatal(err)
		}
	}
	all, n := invocation.BuildSessionContext(svc, session, "")
	if n != 2 {
		t.Fatalf("whole history: %d calls, want 2:\n%s", n, all)
	}
	got, n := invocation.BuildSessionContext(svc, session, "inv_1")
	if n != 1 || !strings.Contains(got, "tool=Read") || strings.Contains(got, "tool=Bash") || strings.Contains(got, "omitted") {
		t.Fatalf("history without inv_1: %d calls, want only the Read call:\n%s", n, got)
	}
}
