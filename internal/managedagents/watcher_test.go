package managedagents

import (
	"context"
	"encoding/json"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"

	"atryum/internal/invocation"
)

// --- fakes ---

type fakeGateway struct {
	mu         sync.Mutex
	submitted  []invocation.ExternalSubmitRequest
	statusByID map[string]invocation.Status
	denyReason map[string]string
	executions []execCall
	nextID     int
}

type execCall struct {
	ID     string
	Update invocation.ExternalExecutionUpdate
}

func newFakeGateway() *fakeGateway {
	return &fakeGateway{statusByID: map[string]invocation.Status{}, denyReason: map[string]string{}}
}

func (g *fakeGateway) Submit(ctx context.Context, req invocation.ExternalSubmitRequest) (invocation.InvocationResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.submitted = append(g.submitted, req)
	g.nextID++
	id := "inv_test_" + itoa(g.nextID)
	status, ok := g.statusByID[*req.IdempotencyKey]
	if !ok {
		status = invocation.StatusPendingApproval
	}
	g.statusByID[id] = status
	if reason, ok := g.denyReason[*req.IdempotencyKey]; ok {
		g.denyReason[id] = reason
	}
	return invocation.InvocationResponse{InvocationID: id, Status: status}, nil
}

func (g *fakeGateway) Get(ctx context.Context, id string) (invocation.InvocationResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	resp := invocation.InvocationResponse{InvocationID: id, Status: g.statusByID[id]}
	if reason, ok := g.denyReason[id]; ok {
		resp.Approval = &invocation.Approval{Status: "auto_denied", Reason: &reason}
	}
	return resp, nil
}

func (g *fakeGateway) RecordExecution(ctx context.Context, id string, update invocation.ExternalExecutionUpdate) (invocation.InvocationResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.executions = append(g.executions, execCall{ID: id, Update: update})
	return invocation.InvocationResponse{InvocationID: id}, nil
}

func (g *fakeGateway) setStatusForKey(key string, status invocation.Status) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.statusByID[key] = status
}

func (g *fakeGateway) submitCount() int { g.mu.Lock(); defer g.mu.Unlock(); return len(g.submitted) }

type fakeClient struct {
	mu   sync.Mutex
	sent []sentEvent
}

type sentEvent struct {
	SessionID string
	Events    []OutboundEvent
}

func (c *fakeClient) ListEventsSince(ctx context.Context, sessionID, after string) ([]RawEvent, error) {
	return nil, nil
}
func (c *fakeClient) StreamEvents(ctx context.Context, sessionID string) (EventStream, error) {
	return nil, context.Canceled
}
func (c *fakeClient) SendEvents(ctx context.Context, sessionID string, events []OutboundEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, sentEvent{SessionID: sessionID, Events: events})
	return nil
}
func (c *fakeClient) sentEvents() []sentEvent { c.mu.Lock(); defer c.mu.Unlock(); return c.sent }

type eofStreamClient struct{}

func (c eofStreamClient) ListEventsSince(ctx context.Context, sessionID, after string) ([]RawEvent, error) {
	return nil, nil
}
func (c eofStreamClient) StreamEvents(ctx context.Context, sessionID string) (EventStream, error) {
	return eofStream{}, nil
}
func (c eofStreamClient) SendEvents(ctx context.Context, sessionID string, events []OutboundEvent) error {
	return nil
}

type eofStream struct{}

func (s eofStream) Next(ctx context.Context) (RawEvent, error) { return RawEvent{}, io.EOF }
func (s eofStream) Close() error                               { return nil }

type fakeSessionStore struct {
	mu      sync.Mutex
	cursors map[string]string
}

func (s *fakeSessionStore) Upsert(ctx context.Context, r SessionRegistration) error { return nil }
func (s *fakeSessionStore) Get(ctx context.Context, id string) (SessionRegistration, error) {
	return SessionRegistration{SessionID: id}, nil
}
func (s *fakeSessionStore) List(ctx context.Context) ([]SessionRegistration, error) { return nil, nil }
func (s *fakeSessionStore) UpdateCursor(ctx context.Context, id, lastEventID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cursors == nil {
		s.cursors = map[string]string{}
	}
	s.cursors[id] = lastEventID
	return nil
}

type fakeAudit struct {
	mu     sync.Mutex
	byKey  map[string]invocation.Invocation
	events []invocation.Event
}

func newFakeAudit() *fakeAudit {
	return &fakeAudit{byKey: map[string]invocation.Invocation{}}
}
func (a *fakeAudit) Create(ctx context.Context, inv invocation.Invocation) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if inv.IdempotencyKey != nil {
		a.byKey[*inv.IdempotencyKey] = inv
	}
	return nil
}
func (a *fakeAudit) GetByIdempotencyKey(ctx context.Context, key string) (invocation.Invocation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if inv, ok := a.byKey[key]; ok {
		return inv, nil
	}
	return invocation.Invocation{}, errNotFound
}
func (a *fakeAudit) CreateEvent(ctx context.Context, evt invocation.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, evt)
	return nil
}
func (a *fakeAudit) eventCount() int { a.mu.Lock(); defer a.mu.Unlock(); return len(a.events) }

var errNotFound = &notFoundError{}

type notFoundError struct{}

func (e *notFoundError) Error() string { return "not found" }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// --- helpers ---

func newTestWatcher(g InvocationGateway, c AnthropicClient, a InvocationAuditStore) *watcher {
	cfg := Config{PollInterval: time.Millisecond}.withDefaults()
	svc, err := NewService(g, &fakeSessionStore{}, a, []Account{{Client: c, Config: cfg}})
	if err != nil {
		panic(err)
	}
	acct := svc.accounts[cfg.Name]
	return &watcher{svc: svc, acct: acct, reg: SessionRegistration{SessionID: "sess_1", Account: cfg.Name, AgentID: "agent-1"}, pending: map[string]pendingCall{}}
}

func TestNewServiceRejectsDuplicateAccountNames(t *testing.T) {
	_, err := NewService(nil, nil, nil, []Account{
		{Config: Config{Name: "prod"}},
		{Config: Config{Name: "prod"}},
	})
	if err == nil {
		t.Fatal("expected duplicate explicit account name error")
	}

	_, err = NewService(nil, nil, nil, []Account{
		{Config: Config{}},
		{Config: Config{}},
	})
	if err == nil {
		t.Fatal("expected duplicate default account name error")
	}
}

func TestFollowDoesNotLeakCloseGoroutineAfterEOF(t *testing.T) {
	w := newTestWatcher(newFakeGateway(), eofStreamClient{}, newFakeAudit())
	ctx := context.Background()

	runtime.GC()
	before := runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		if err := w.follow(ctx); err != nil {
			t.Fatalf("follow %d: %v", i, err)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= before+5 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("possible close goroutine leak: before=%d after=%d", before, runtime.NumGoroutine())
}

func toolUseEvent(id, eventType, name string, input map[string]any) RawEvent {
	body := map[string]any{"id": id, "type": eventType, "name": name, "input": input}
	raw, _ := json.Marshal(body)
	return RawEvent{ID: id, Type: eventType, Raw: raw, ProcessedAt: time.Now()}
}

func idleRequiresAction(id string, blockingIDs []string) RawEvent {
	body := map[string]any{"id": id, "type": evtSessionIdle, "stop_reason": map[string]any{"type": stopReasonRequiresAction, "event_ids": blockingIDs}}
	raw, _ := json.Marshal(body)
	return RawEvent{ID: id, Type: evtSessionIdle, Raw: raw, ProcessedAt: time.Now()}
}

// --- tests ---

func TestToolUseApprovedSendsAllow(t *testing.T) {
	g := newFakeGateway()
	g.statusByID["tool_evt_1"] = invocation.StatusPendingApproval
	c := &fakeClient{}
	a := newFakeAudit()
	w := newTestWatcher(g, c, a)
	ctx := context.Background()

	w.handleEvent(ctx, toolUseEvent("tool_evt_1", evtAgentToolUse, "Bash", map[string]any{"command": "ls"}))
	if g.submitCount() != 1 {
		t.Fatalf("expected 1 submit, got %d", g.submitCount())
	}
	// Approve the pending invocation, then deliver requires_action.
	w.mu.Lock()
	invID := w.pending["tool_evt_1"].InvocationID
	w.mu.Unlock()
	g.setStatusForKey(invID, invocation.StatusApproved)

	w.handleEvent(ctx, idleRequiresAction("idle_1", []string{"tool_evt_1"}))

	sent := c.sentEvents()
	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound event, got %d", len(sent))
	}
	evt := sent[0].Events[0]
	if evt.Type != "user.tool_confirmation" || evt.Fields["result"] != "allow" {
		t.Fatalf("expected allow tool_confirmation, got %+v", evt)
	}
	if evt.Fields["tool_use_id"] != "tool_evt_1" {
		t.Fatalf("wrong tool_use_id: %v", evt.Fields["tool_use_id"])
	}
}

func TestToolUseDeniedSendsDeny(t *testing.T) {
	g := newFakeGateway()
	g.statusByID["tool_evt_1"] = invocation.StatusDenied
	g.denyReason["tool_evt_1"] = "matched approval rule (auto_deny)"
	c := &fakeClient{}
	w := newTestWatcher(g, c, newFakeAudit())
	ctx := context.Background()

	w.handleEvent(ctx, toolUseEvent("tool_evt_1", evtAgentMCPToolUse, "delete_repo", nil))
	w.handleEvent(ctx, idleRequiresAction("idle_1", []string{"tool_evt_1"}))

	sent := c.sentEvents()
	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound event, got %d", len(sent))
	}
	evt := sent[0].Events[0]
	if evt.Type != "user.tool_confirmation" || evt.Fields["result"] != "deny" {
		t.Fatalf("expected deny tool_confirmation, got %+v", evt)
	}
	if evt.Fields["deny_message"] != "matched approval rule (auto_deny)" {
		t.Fatalf("wrong deny_message: %v", evt.Fields["deny_message"])
	}
}

func TestCustomToolApprovedSendsCustomResult(t *testing.T) {
	g := newFakeGateway()
	g.statusByID["custom_evt_1"] = invocation.StatusApproved
	c := &fakeClient{}
	w := newTestWatcher(g, c, newFakeAudit())
	ctx := context.Background()

	w.handleEvent(ctx, toolUseEvent("custom_evt_1", evtAgentCustomToolUse, "deploy", map[string]any{"env": "prod"}))
	w.handleEvent(ctx, idleRequiresAction("idle_1", []string{"custom_evt_1"}))

	sent := c.sentEvents()
	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound event, got %d", len(sent))
	}
	evt := sent[0].Events[0]
	if evt.Type != "user.custom_tool_result" {
		t.Fatalf("expected custom_tool_result, got %+v", evt)
	}
	if evt.Fields["custom_tool_use_id"] != "custom_evt_1" {
		t.Fatalf("wrong custom_tool_use_id: %v", evt.Fields["custom_tool_use_id"])
	}
}

func TestEveryEventAudited(t *testing.T) {
	g := newFakeGateway()
	a := newFakeAudit()
	w := newTestWatcher(g, &fakeClient{}, a)
	ctx := context.Background()

	w.handleEvent(ctx, RawEvent{ID: "e1", Type: "agent.message", Raw: json.RawMessage(`{"type":"agent.message"}`), ProcessedAt: time.Now()})
	w.handleEvent(ctx, RawEvent{ID: "e2", Type: "session.status_running", Raw: json.RawMessage(`{"type":"session.status_running"}`), ProcessedAt: time.Now()})

	if a.eventCount() != 2 {
		t.Fatalf("expected 2 audited events, got %d", a.eventCount())
	}
	if g.submitCount() != 0 {
		t.Fatalf("non-tool events should not create invocations, got %d", g.submitCount())
	}
}

func TestMultipleBlockingIDsResolved(t *testing.T) {
	g := newFakeGateway()
	g.statusByID["t1"] = invocation.StatusApproved
	g.statusByID["t2"] = invocation.StatusApproved
	c := &fakeClient{}
	w := newTestWatcher(g, c, newFakeAudit())
	ctx := context.Background()

	w.handleEvent(ctx, toolUseEvent("t1", evtAgentToolUse, "Read", nil))
	w.handleEvent(ctx, toolUseEvent("t2", evtAgentToolUse, "Write", nil))
	w.handleEvent(ctx, idleRequiresAction("idle_1", []string{"t1", "t2"}))

	if len(c.sentEvents()) != 2 {
		t.Fatalf("expected 2 confirmations, got %d", len(c.sentEvents()))
	}
}

func TestToolResultRecordsExecution(t *testing.T) {
	g := newFakeGateway()
	g.statusByID["t1"] = invocation.StatusApproved
	c := &fakeClient{}
	w := newTestWatcher(g, c, newFakeAudit())
	ctx := context.Background()

	w.handleEvent(ctx, toolUseEvent("t1", evtAgentToolUse, "Bash", nil))
	resultBody, _ := json.Marshal(map[string]any{"id": "r1", "type": "agent.tool_result", "tool_use_id": "t1", "content": "done"})
	w.handleEvent(ctx, RawEvent{ID: "r1", Type: "agent.tool_result", Raw: resultBody, ProcessedAt: time.Now()})

	g.mu.Lock()
	defer g.mu.Unlock()
	foundCompleted := false
	for _, e := range g.executions {
		if e.Update.ExecutionStatus == "completed" {
			foundCompleted = true
		}
	}
	if !foundCompleted {
		t.Fatalf("expected a completed RecordExecution, got %+v", g.executions)
	}
}

func TestDuplicateToolUseUsesIdempotencyKey(t *testing.T) {
	g := newFakeGateway()
	c := &fakeClient{}
	w := newTestWatcher(g, c, newFakeAudit())
	ctx := context.Background()

	evt := toolUseEvent("t1", evtAgentToolUse, "Bash", nil)
	w.handleEvent(ctx, evt)
	w.handleEvent(ctx, evt) // replay after reconnect

	// Both submits carry the same idempotency key; the real service dedupes.
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, req := range g.submitted {
		if req.IdempotencyKey == nil || *req.IdempotencyKey != "t1" {
			t.Fatalf("expected idempotency key t1, got %v", req.IdempotencyKey)
		}
	}
}
