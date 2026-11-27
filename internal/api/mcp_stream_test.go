package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/mcp"
)

// upstreamInvokeService runs tools/call through a real upstream client, so
// the handler's stream sink reaches the transport as it does in production.
type upstreamInvokeService struct {
	*stubService
	client   *mcp.Client
	upstream mcp.Upstream
}

func (s *upstreamInvokeService) Invoke(ctx context.Context, req invocation.CreateInvocationRequest) (invocation.InvocationResponse, error) {
	result, err := s.client.Invoke(ctx, s.upstream, req.Tool, req.Input, req.RequestID)
	if err != nil {
		return invocation.InvocationResponse{}, err
	}
	return invocation.InvocationResponse{InvocationID: "inv_stream", Status: invocation.StatusSucceeded, Result: result.Body}, nil
}

func TestMCPToolsCallRelaysUpstreamSSELive(t *testing.T) {
	release := make(chan struct{})
	var terminalSent atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req mcp.Envelope
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"capabilities": map[string]any{}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"p1\",\"progress\":1}}\n\n"))
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-time.After(5 * time.Second):
			}
			terminalSent.Store(true)
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}\n\n"))
		}
	}))
	defer upstream.Close()

	svc := &upstreamInvokeService{stubService: &stubService{}, client: mcp.NewHTTPClient(), upstream: mcp.Upstream{Name: "slow", Mode: mcp.UpstreamModeHTTP, BaseURL: upstream.URL}}
	atryum := httptest.NewServer(NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil).Routes())
	defer atryum.Close()

	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"work","arguments":{},"_meta":{"progressToken":"p1"}}}`
	req, _ := http.NewRequest(http.MethodPost, atryum.URL+"/mcp/slow", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}

	events := readSSEEvents(resp.Body)
	first := <-events
	if terminalSent.Load() {
		t.Fatal("the agent saw the first event only after the upstream sent its response")
	}
	if !strings.Contains(first, `"notifications/progress"`) || !strings.Contains(first, `"progressToken":"p1"`) {
		t.Fatalf("first event = %q", first)
	}
	close(release)
	last := <-events
	var final struct {
		ID     json.RawMessage `json:"id"`
		Result struct {
			Content []struct{ Text string } `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(last), &final); err != nil || string(final.ID) != "7" || len(final.Result.Content) != 1 || final.Result.Content[0].Text != "done" {
		t.Fatalf("last event = %q (%v)", last, err)
	}
	if extra, ok := <-events; ok {
		t.Fatalf("stream continued after the response: %q", extra)
	}
}

// A tools/call whose upstream answers with plain JSON gets plain JSON back.
func TestMCPToolsCallJSONUpstreamStaysJSON(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req mcp.Envelope
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "plain"}}}})
	}))
	defer upstream.Close()

	svc := &upstreamInvokeService{stubService: &stubService{}, client: mcp.NewHTTPClient(), upstream: mcp.Upstream{Name: "json", Mode: mcp.UpstreamModeHTTP, BaseURL: upstream.URL}}
	w := httptest.NewRecorder()
	NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil).Routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mcp/json", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"work","arguments":{}}}`)))
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q", ct)
	}
	if !strings.Contains(w.Body.String(), `"id":3`) || !strings.Contains(w.Body.String(), `"text":"plain"`) {
		t.Fatalf("body = %s", w.Body.String())
	}
}

// readSSEEvents sends each SSE event's joined data as it is read, and closes
// the channel at the end of the stream.
func readSSEEvents(body interface{ Read([]byte) (int, error) }) <-chan string {
	out := make(chan string)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(body)
		var data []string
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if len(data) > 0 {
					out <- strings.Join(data, "\n")
					data = nil
				}
				continue
			}
			if v, ok := strings.CutPrefix(line, "data: "); ok {
				data = append(data, v)
			}
		}
	}()
	return out
}
