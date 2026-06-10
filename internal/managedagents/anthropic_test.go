package managedagents

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListEventsSinceFiltersByCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("anthropic-beta"); got != managedAgentsBeta {
			t.Errorf("missing beta header, got %q", got)
		}
		fmt.Fprint(w, `{"data":[
			{"id":"e1","type":"agent.message","processed_at":"2026-04-01T00:00:00Z"},
			{"id":"e2","type":"agent.tool_use","processed_at":"2026-04-01T00:00:01Z"},
			{"id":"e3","type":"session.status_idle","processed_at":"2026-04-01T00:00:02Z"}
		]}`)
	}))
	defer srv.Close()

	c := NewAnthropicHTTPClient(Config{BaseURL: srv.URL, APIKey: "k"})
	events, err := c.ListEventsSince(context.Background(), "sess_1", "e1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events after cursor e1, got %d", len(events))
	}
	if events[0].ID != "e2" || events[1].ID != "e3" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestStreamEventsParsesSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keep-alive ping\n")
		fmt.Fprint(w, "event: message\n")
		fmt.Fprint(w, `data: {"id":"e1","type":"agent.tool_use","name":"Bash"}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"e2","type":"session.status_idle"}`+"\n\n")
	}))
	defer srv.Close()

	c := NewAnthropicHTTPClient(Config{BaseURL: srv.URL, APIKey: "k"})
	stream, err := c.StreamEvents(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer stream.Close()

	e1, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("next 1: %v", err)
	}
	if e1.ID != "e1" || e1.Type != "agent.tool_use" {
		t.Fatalf("unexpected first event: %+v", e1)
	}
	e2, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("next 2: %v", err)
	}
	if e2.ID != "e2" {
		t.Fatalf("unexpected second event: %+v", e2)
	}
}

func TestStreamEventsParsesMultilineSSEData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message\n")
		fmt.Fprint(w, "data: {\"id\":\"e1\",\n")
		fmt.Fprint(w, "data: \"type\":\"agent.tool_use\",\n")
		fmt.Fprint(w, "data: \"name\":\"Bash\"}\n\n")
	}))
	defer srv.Close()

	c := NewAnthropicHTTPClient(Config{BaseURL: srv.URL, APIKey: "k"})
	stream, err := c.StreamEvents(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer stream.Close()

	evt, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if evt.ID != "e1" || evt.Type != "agent.tool_use" {
		t.Fatalf("unexpected event: %+v raw=%s", evt, string(evt.Raw))
	}
}

func TestSendEventsPostsFlattenedType(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := NewAnthropicHTTPClient(Config{BaseURL: srv.URL, APIKey: "k"})
	err := c.SendEvents(context.Background(), "sess_1", []OutboundEvent{{
		Type:   "user.tool_confirmation",
		Fields: map[string]any{"tool_use_id": "t1", "result": "allow"},
	}})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !strings.Contains(gotBody, `"type":"user.tool_confirmation"`) || !strings.Contains(gotBody, `"result":"allow"`) {
		t.Fatalf("unexpected body: %s", gotBody)
	}
}
