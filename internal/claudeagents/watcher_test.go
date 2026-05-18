package claudeagents

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type memSessionStore struct {
	mu       sync.Mutex
	sessions []SessionRecord
	updates  map[string]string // sessionRowID -> cursor
}

func (m *memSessionStore) ListWatchableSessions(context.Context) ([]SessionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SessionRecord, len(m.sessions))
	copy(out, m.sessions)
	return out, nil
}

func (m *memSessionStore) UpdateSessionCursor(_ context.Context, id, cursor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updates == nil {
		m.updates = map[string]string{}
	}
	m.updates[id] = cursor
	for i := range m.sessions {
		if m.sessions[i].ID == id {
			m.sessions[i].LastEventCursor = cursor
		}
	}
	return nil
}

func (m *memSessionStore) EnsureAgent(_ context.Context, _, _, _ string) error   { return nil }
func (m *memSessionStore) EnsureSession(_ context.Context, _, _ string) error    { return nil }

// fakeAPI serves a configurable sequence of GET responses. Each response is
// a JSON array body for the `data` field. After exhausting the slice it
// keeps returning the last entry so subsequent ticks observe the same data.
type fakeAPI struct {
	mu      sync.Mutex
	batches []string
	calls   int
	queries []string
}

func newFakeAPI(t *testing.T, batches []string) (*Client, *fakeAPI) {
	t.Helper()
	f := &fakeAPI{batches: batches}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Discovery endpoints (list agents, list sessions) are served as
		// empty so test fixtures only have to describe the events stream.
		if r.Method == http.MethodGet && !strings.Contains(r.URL.Path, "/events") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[]}`)
			return
		}

		f.mu.Lock()
		idx := f.calls
		if idx >= len(f.batches) {
			idx = len(f.batches) - 1
		}
		body := f.batches[idx]
		f.calls++
		f.queries = append(f.queries, r.URL.RawQuery)
		f.mu.Unlock()

		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":`+body+`}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"evt_ack"}`)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Options{APIKey: "k", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, f
}

func newWatcherForTest(t *testing.T, c *Client, store SessionStore, h ToolUseHandler) *Watcher {
	t.Helper()
	w, err := NewWatcher(WatcherOptions{
		Client:       c,
		Store:        store,
		Handler:      h,
		PollInterval: time.Hour, // tickOnce drives the test directly
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	return w
}

func TestWatcher_DispatchesRequiresActionAndAdvancesCursor(t *testing.T) {
	body := `[
		{"id":"toolu_1","type":"agent.tool_use","created_at":"2026-05-14T10:00:00Z","name":"read","input":{"path":"/x"}},
		{"id":"idle_1","type":"session.status_idle","created_at":"2026-05-14T10:00:01Z","stop_reason":{"type":"requires_action","event_ids":["toolu_1"]}}
	]`
	client, fake := newFakeAPI(t, []string{body, `[]`})
	store := &memSessionStore{
		sessions: []SessionRecord{{
			ID: "sess_row_1", AgentID: "agent_a", AgentName: "PR Reviewer",
			SessionID: "sess_xyz",
		}},
	}

	var got []ToolUseEvent
	var mu sync.Mutex
	handler := func(_ context.Context, evt ToolUseEvent) error {
		mu.Lock()
		got = append(got, evt)
		mu.Unlock()
		return nil
	}

	w := newWatcherForTest(t, client, store, handler)
	w.tickOnce(context.Background())

	if len(got) != 1 {
		t.Fatalf("dispatched %d events, want 1: %+v", len(got), got)
	}
	if got[0].ToolName != "read" || got[0].ToolUseEventID != "toolu_1" {
		t.Errorf("event = %+v", got[0])
	}
	if got[0].AgentName != "PR Reviewer" || got[0].SessionID != "sess_xyz" {
		t.Errorf("agent/session mismatch: %+v", got[0])
	}
	var input map[string]any
	_ = json.Unmarshal(got[0].Input, &input)
	if input["path"] != "/x" {
		t.Errorf("input mismatch: %+v", input)
	}

	// Cursor should have advanced to the idle's created_at.
	wantCursor := "2026-05-14T10:00:01Z"
	if !strings.HasPrefix(store.updates["sess_row_1"], wantCursor) {
		t.Errorf("cursor = %q, want prefix %q", store.updates["sess_row_1"], wantCursor)
	}

	// A second tick (returns []) should not re-dispatch the same event.
	got = nil
	w.tickOnce(context.Background())
	if len(got) != 0 {
		t.Errorf("re-dispatched %d events on second tick", len(got))
	}

	// And the request after the cursor should include created_at[gt].
	if last := fake.queries[len(fake.queries)-1]; !strings.Contains(last, "created_at%5Bgt%5D=") {
		t.Errorf("second-tick query missing cursor: %q", last)
	}
}

func TestWatcher_SkipsIdleEventsWithoutRequiresAction(t *testing.T) {
	body := `[
		{"id":"idle_1","type":"session.status_idle","created_at":"2026-05-14T10:00:00Z","stop_reason":{"type":"end_turn"}}
	]`
	client, _ := newFakeAPI(t, []string{body})
	store := &memSessionStore{
		sessions: []SessionRecord{{
			ID: "sess_row_1", AgentID: "agent_a", AgentName: "A", SessionID: "sess_xyz",
		}},
	}

	called := 0
	w := newWatcherForTest(t, client, store, func(context.Context, ToolUseEvent) error {
		called++
		return nil
	})
	w.tickOnce(context.Background())
	if called != 0 {
		t.Fatalf("handler called %d times", called)
	}
	// Cursor still advances so we don't re-fetch the same idle forever.
	if store.updates["sess_row_1"] == "" {
		t.Errorf("cursor was not advanced past non-blocking idle")
	}
}

func TestWatcher_PausesCursorWhenToolUseEventNotInBatch(t *testing.T) {
	// status_idle references toolu_1 but toolu_1 is not in this batch.
	body := `[
		{"id":"idle_1","type":"session.status_idle","created_at":"2026-05-14T10:00:01Z","stop_reason":{"type":"requires_action","event_ids":["toolu_1"]}}
	]`
	client, _ := newFakeAPI(t, []string{body})
	store := &memSessionStore{
		sessions: []SessionRecord{{
			ID: "sess_row_1", AgentID: "agent_a", AgentName: "A", SessionID: "sess_xyz",
		}},
	}
	called := 0
	w := newWatcherForTest(t, client, store, func(context.Context, ToolUseEvent) error {
		called++
		return nil
	})
	w.tickOnce(context.Background())
	if called != 0 {
		t.Fatalf("handler called %d times", called)
	}
	if store.updates["sess_row_1"] != "" {
		t.Errorf("cursor advanced past unresolved idle: %q", store.updates["sess_row_1"])
	}
}

func TestWatcher_HandlerErrorBlocksCursorAdvance(t *testing.T) {
	body := `[
		{"id":"toolu_1","type":"agent.tool_use","created_at":"2026-05-14T10:00:00Z","name":"read","input":{}},
		{"id":"idle_1","type":"session.status_idle","created_at":"2026-05-14T10:00:01Z","stop_reason":{"type":"requires_action","event_ids":["toolu_1"]}}
	]`
	client, _ := newFakeAPI(t, []string{body, body})
	store := &memSessionStore{
		sessions: []SessionRecord{{
			ID: "sess_row_1", AgentID: "agent_a", AgentName: "A", SessionID: "sess_xyz",
		}},
	}

	var attempts int
	w := newWatcherForTest(t, client, store, func(context.Context, ToolUseEvent) error {
		attempts++
		return errFakeHandler
	})
	w.tickOnce(context.Background())
	if attempts != 1 {
		t.Fatalf("handler attempts = %d", attempts)
	}
	if store.updates["sess_row_1"] != "" {
		t.Errorf("cursor advanced after handler error: %q", store.updates["sess_row_1"])
	}
}

func TestWatcher_TagsMCPToolUse(t *testing.T) {
	body := `[
		{"id":"toolu_1","type":"agent.mcp_tool_use","created_at":"2026-05-14T10:00:00Z","name":"github__create_issue","input":{}},
		{"id":"idle_1","type":"session.status_idle","created_at":"2026-05-14T10:00:01Z","stop_reason":{"type":"requires_action","event_ids":["toolu_1"]}}
	]`
	client, _ := newFakeAPI(t, []string{body})
	store := &memSessionStore{
		sessions: []SessionRecord{{
			ID: "sess_row_1", AgentID: "agent_a", AgentName: "A", SessionID: "sess_xyz",
		}},
	}
	var got ToolUseEvent
	w := newWatcherForTest(t, client, store, func(_ context.Context, evt ToolUseEvent) error {
		got = evt
		return nil
	})
	w.tickOnce(context.Background())
	if !got.IsMCP {
		t.Fatalf("expected IsMCP=true, got %+v", got)
	}
}

var errFakeHandler = errFakeHandlerType("handler boom")

type errFakeHandlerType string

func (e errFakeHandlerType) Error() string { return string(e) }
