package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// multiFrameSSEHandler mirrors integrations/fixtures/mock-streaming-mcp/streaming_mcp_server.py
func multiFrameSSEHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			writeTestRPC(w, req.ID, map[string]any{
				"protocolVersion": r.Header.Get("MCP-Protocol-Version"),
				"capabilities":    map[string]any{},
			}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			final, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "STREAM_FINAL:42"}},
				},
			})
			if err != nil {
				t.Fatalf("marshal final frame: %v", err)
			}
			writeTestSSEEvents(w,
				[]string{`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":0.25}}`},
				[]string{`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":0.75}}`},
				[]string{string(final)},
			)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	})
}

func TestInvokeExtractsTerminalFrameFromMultiFrameSSE(t *testing.T) {
	server := httptest.NewServer(multiFrameSSEHandler(t))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	result, err := client.Invoke(t.Context(), Upstream{
		Name:    "streaming-mock",
		Mode:    UpstreamModeHTTP,
		BaseURL: server.URL,
		Enabled: true,
	}, "stream_echo", map[string]any{"value": 42}, nil)
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if !strings.Contains(string(result.Body), "STREAM_FINAL:42") {
		t.Fatalf("expected terminal frame body, got %s", string(result.Body))
	}
	if strings.Contains(string(result.Body), "notifications/progress") {
		t.Fatalf("expected collapsed terminal response, got progress notification payload: %s", string(result.Body))
	}
}

func TestDecodeJSONRPCPayloadSkipsProgressFrames(t *testing.T) {
	sseBody := []byte(
		"event: message\n" +
			`data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":0.25}}` + "\n\n" +
			"event: message\n" +
			`data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":0.75}}` + "\n\n" +
			"event: message\n" +
			`data: {"jsonrpc":"2.0","id":4,"result":{"content":[{"type":"text","text":"STREAM_FINAL:99"}]}}` + "\n\n",
	)
	body, err := DecodeJSONRPCPayload(ForwardResult{
		StatusCode:  http.StatusOK,
		Body:        sseBody,
		ContentType: "text/event-stream",
	}, json.RawMessage([]byte("4")))
	if err != nil {
		t.Fatalf("DecodeJSONRPCPayload: %v", err)
	}
	if !strings.Contains(string(body), "STREAM_FINAL:99") {
		t.Fatalf("expected terminal payload, got %s", string(body))
	}
}