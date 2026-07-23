package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListToolsDecodesMultilineSSEData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-multiline")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeTestSSEEvents(w, []string{
				`{"jsonrpc":"2.0",`,
				`"id":1,`,
				`"result":{"tools":[{"name":"stories.multiline"}]}}`,
			})
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	tools, err := client.ListTools(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "stories.multiline" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
}

func TestSSEEventReaderSaturatesOversizedRetryWithoutOverflow(t *testing.T) {
	reader := newSSEEventReader(strings.NewReader("retry: 9223372036854775807\n\n"))
	evt, err := reader.NextEvent()
	if err != nil {
		t.Fatal(err)
	}
	if !evt.HasRetry {
		t.Fatal("expected retry field to be parsed")
	}
	if evt.Retry <= 0 {
		t.Fatalf("oversized retry overflowed to %s; want a positive saturated duration", evt.Retry)
	}
}

func TestSSEEventReaderRejectsAggregateEventOverLimit(t *testing.T) {
	reader := newSSEEventReaderWithLimit(
		strings.NewReader("data: 12345678\ndata: 12345678\n\n"),
		16,
	)

	_, err := reader.NextEvent()
	if !errors.Is(err, ErrStreamMessageTooLarge) {
		t.Fatalf("NextEvent error = %v, want ErrStreamMessageTooLarge", err)
	}
}

func TestSSEEventReaderRejectsSingleLineOverLimitWithTypedError(t *testing.T) {
	reader := newSSEEventReaderWithLimit(strings.NewReader("data: 1234567890\n\n"), 12)

	_, err := reader.NextEvent()
	if !errors.Is(err, ErrStreamMessageTooLarge) {
		t.Fatalf("NextEvent error = %v, want ErrStreamMessageTooLarge", err)
	}
}

func TestSSEEventReaderDoesNotAccumulateCommentBytesAcrossEvents(t *testing.T) {
	reader := newSSEEventReaderWithLimit(
		strings.NewReader(": ping\n\n: ping\n\n: ping\n\ndata: ok\n\n"),
		12,
	)

	evt, err := reader.NextEvent()
	if err != nil {
		t.Fatalf("NextEvent returned error after bounded comment events: %v", err)
	}
	if string(evt.Data) != "ok" {
		t.Fatalf("event data = %q, want ok", evt.Data)
	}
}
