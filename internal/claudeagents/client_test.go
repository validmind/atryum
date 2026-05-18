package claudeagents

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Options{APIKey: "test-key", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv
}

func TestNew_RequiresAPIKey(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("expected error for missing api key")
	}
}

func TestListEvents_BuildsRequestAndDecodes(t *testing.T) {
	var gotPath string
	var gotQuery string
	var gotHeaders http.Header

	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotHeaders = r.Header
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"data":[
			{"id":"evt_1","type":"session.status_idle","created_at":"2026-05-14T10:00:00Z","stop_reason":{"type":"requires_action","event_ids":["toolu_1"]}},
			{"id":"toolu_1","type":"agent.tool_use","created_at":"2026-05-14T10:00:01Z","tool_use_id":"toolu_1","name":"read","input":{"path":"/etc/hosts"}}
		]}`)
	})

	after := time.Date(2026, 5, 14, 9, 0, 0, 0, time.UTC)
	events, err := c.ListEvents(context.Background(), "sess_xyz", ListEventsOptions{
		Types:        []string{"session.status_idle", "agent.tool_use"},
		CreatedAfter: after,
		Limit:        50,
		Order:        "asc",
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}

	if gotPath != "/v1/sessions/sess_xyz/events" {
		t.Errorf("path = %q", gotPath)
	}
	for _, want := range []string{"types%5B%5D=session.status_idle", "types%5B%5D=agent.tool_use", "limit=50", "order=asc", "created_at%5Bgt%5D="} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query missing %q: %q", want, gotQuery)
		}
	}
	if gotHeaders.Get("x-api-key") != "test-key" {
		t.Errorf("missing x-api-key header: %q", gotHeaders.Get("x-api-key"))
	}
	if gotHeaders.Get("anthropic-version") == "" {
		t.Errorf("missing anthropic-version header")
	}
	if gotHeaders.Get("anthropic-beta") == "" {
		t.Errorf("missing anthropic-beta header")
	}

	if len(events) != 2 {
		t.Fatalf("event count = %d", len(events))
	}
	if events[0].StopReason == nil || events[0].StopReason.Type != "requires_action" {
		t.Errorf("event[0].stop_reason = %+v", events[0].StopReason)
	}
	if events[1].Name != "read" {
		t.Errorf("event[1].name = %q", events[1].Name)
	}
	if len(events[1].Input) == 0 {
		t.Errorf("event[1].input is empty")
	}
	if len(events[0].Raw) == 0 {
		t.Errorf("event[0].Raw should be populated")
	}
}

func TestListEvents_NonOKReturnsError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"boom"}`)
	})
	if _, err := c.ListEvents(context.Background(), "sess_x", ListEventsOptions{}); err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestSendToolConfirmation_AllowAndDenyShapes(t *testing.T) {
	type recorded struct {
		path string
		body sendEventsRequest
	}
	var got []recorded

	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body sendEventsRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		got = append(got, recorded{path: r.URL.Path, body: body})
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"evt_ack"}`)
	})

	if err := c.SendToolConfirmation(context.Background(), "sess_a", "toolu_1", ResultAllow, "ignored"); err != nil {
		t.Fatalf("allow: %v", err)
	}
	if err := c.SendToolConfirmation(context.Background(), "sess_a", "toolu_2", ResultDeny, "use staging"); err != nil {
		t.Fatalf("deny: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("recorded %d calls", len(got))
	}
	if len(got[0].body.Events) != 1 || len(got[1].body.Events) != 1 {
		t.Fatalf("expected exactly one event per call, got %+v", got)
	}
	allow := got[0].body.Events[0]
	deny := got[1].body.Events[0]
	if allow.Type != "user.tool_confirmation" || allow.Result != ResultAllow || allow.ToolUseID != "toolu_1" {
		t.Errorf("allow event = %+v", allow)
	}
	if allow.DenyMessage != "" {
		t.Errorf("allow should not carry deny_message, got %q", allow.DenyMessage)
	}
	if deny.Result != ResultDeny || deny.DenyMessage != "use staging" {
		t.Errorf("deny event = %+v", deny)
	}
	for _, g := range got {
		if g.path != "/v1/sessions/sess_a/events" {
			t.Errorf("path = %q", g.path)
		}
	}
}

func TestSendToolConfirmation_ValidatesArgs(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be called")
		w.WriteHeader(http.StatusOK)
	})
	cases := []struct {
		name, session, toolUse string
		result                 ConfirmationResult
	}{
		{"empty session", "", "toolu_1", ResultAllow},
		{"empty tool use", "sess_a", "", ResultAllow},
		{"bad result", "sess_a", "toolu_1", ConfirmationResult("maybe")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := c.SendToolConfirmation(context.Background(), tc.session, tc.toolUse, tc.result, ""); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
