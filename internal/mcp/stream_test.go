package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// streamingUpstream answers tools/call with an SSE stream: a progress
// notification, a request Atryum cannot broker, then (once release is closed)
// the response. terminalSent is set just before the response is written.
func streamingUpstream(t *testing.T, release <-chan struct{}, terminalSent *atomic.Bool, gotParams chan<- json.RawMessage) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		switch req.Method {
		case "initialize":
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			if gotParams != nil {
				gotParams <- req.Params
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("event: message\nid: up-1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"tok-1\",\"progress\":1,\"total\":2}}\n\n"))
			_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":99,\"method\":\"sampling/createMessage\",\"params\":{}}\n\n"))
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-time.After(5 * time.Second):
			}
			terminalSent.Store(true)
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}\n\n"))
		default:
			t.Errorf("unexpected method %q", req.Method)
		}
	}))
}

func TestInvokeRelaysSSEEventsBeforeTheResponse(t *testing.T) {
	release := make(chan struct{})
	var terminalSent atomic.Bool
	gotParams := make(chan json.RawMessage, 1)
	server := streamingUpstream(t, release, &terminalSent, gotParams)
	defer server.Close()

	var relayed []StreamEvent
	sink := &StreamSink{ProgressToken: json.RawMessage(`"tok-1"`), Emit: func(evt StreamEvent) {
		if terminalSent.Load() {
			t.Error("intermediate event relayed only after the upstream sent its response")
		}
		relayed = append(relayed, evt)
		close(release)
	}}
	client := NewHTTPClient()
	client.httpClient = server.Client()
	requestID := "7"
	result, err := client.Invoke(WithStreamSink(context.Background(), sink), Upstream{Name: "slow", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "work", map[string]any{}, &requestID)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(string(result.Body), `"done"`) || result.Failed {
		t.Fatalf("result = %s failed=%t", result.Body, result.Failed)
	}
	if len(relayed) != 1 || relayed[0].Method != "notifications/progress" || relayed[0].Event != "message" {
		t.Fatalf("relayed = %+v (the sampling request must not be relayed)", relayed)
	}
	if len(result.Stream) != 1 || result.UpstreamRequestID != "1" {
		t.Fatalf("recorded stream = %+v upstream id %q", result.Stream, result.UpstreamRequestID)
	}
	var params struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	_ = json.Unmarshal(<-gotParams, &params)
	if string(params.Meta["progressToken"]) != `"tok-1"` || string(params.Meta["atryumRequestId"]) != `"7"` {
		t.Fatalf("upstream _meta = %v", params.Meta)
	}
}

// Without a sink the stream is still read to its response and recorded.
func TestInvokeRecordsSSEEventsWithoutASink(t *testing.T) {
	release := make(chan struct{})
	close(release)
	var terminalSent atomic.Bool
	server := streamingUpstream(t, release, &terminalSent, nil)
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	result, err := client.Invoke(context.Background(), Upstream{Name: "slow", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "work", map[string]any{}, nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(string(result.Body), `"done"`) || len(result.Stream) != 1 {
		t.Fatalf("body %s stream %+v", result.Body, result.Stream)
	}
}

func TestInvokeJSONResponseIsUnchangedWithASink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			writeTestRPC(w, req.ID, map[string]any{"capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		default:
			writeTestRPC(w, req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": "plain"}}}, nil)
		}
	}))
	defer server.Close()

	sink := &StreamSink{Emit: func(evt StreamEvent) { t.Errorf("unexpected relay %+v", evt) }}
	client := NewHTTPClient()
	client.httpClient = server.Client()
	result, err := client.Invoke(WithStreamSink(context.Background(), sink), Upstream{Name: "json", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "work", map[string]any{}, nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if string(result.Body) != `{"content":[{"text":"plain","type":"text"}]}` || len(result.Stream) != 0 {
		t.Fatalf("body %s stream %+v", result.Body, result.Stream)
	}
}

// A stdio server may write other messages before its tools/call response:
// a request of its own (which carries an id and used to be taken for the
// response) and notifications, which are relayed.
func TestInvokeStdioCorrelatesTheResponseByID(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-mcp.sh")
	content := `#!/usr/bin/env bash
set -euo pipefail
while IFS= read -r line; do
  if [[ "$line" == *'"method":"initialize"'* ]]; then
    id=$(sed -E 's/.*"id":([0-9]+).*/\1/' <<<"$line")
    printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"booting"}}'
    printf '{"jsonrpc":"2.0","id":%s,"result":{"capabilities":{}}}\n' "$id"
  elif [[ "$line" == *'"method":"tools/call"'* ]]; then
    id=$(sed -E 's/.*"id":([0-9]+).*/\1/' <<<"$line")
    printf '%s\n' '{"jsonrpc":"2.0","id":"srv-1","method":"roots/list"}'
    printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}'
    printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"stdio done"}]}}\n' "$id"
    exit 0
  fi
done
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	var relayed []string
	sink := &StreamSink{Emit: func(evt StreamEvent) { relayed = append(relayed, evt.Method) }}
	result, err := NewHTTPClient().Invoke(WithStreamSink(context.Background(), sink), Upstream{Name: "stdio", Mode: UpstreamModeStdio, Command: script}, "work", map[string]any{}, nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(string(result.Body), "stdio done") {
		t.Fatalf("body = %s", result.Body)
	}
	if len(relayed) != 1 || relayed[0] != "notifications/progress" {
		t.Fatalf("relayed = %v", relayed)
	}
}
