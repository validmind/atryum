//go:build mcpeverything

// Real end-to-end test against the official MCP reference "everything"
// server (@modelcontextprotocol/server-everything), run over its Streamable
// HTTP transport, through a real running Atryum handler stack. Requires
// Node/npm (npx) and, on first run, network access to fetch the package —
// excluded from `go test ./...` via this build tag so the default suite
// stays fast and hermetic. Run explicitly:
//
//	just mcp-everything-test
//
// or:
//
//	go test -tags mcpeverything ./internal/api -run TestMCPToolsCallAgainstRealEverythingServer -v
//
// trigger-long-running-operation's progress notification carries
// relatedRequestId, so the reference server routes it onto the same
// connection as the tools/call response — this test proves that path. It
// does not exercise the standalone-channel path (see
// internal/mcp/client_test.go's TestInvokeStreamStandaloneStream* tests,
// and mcp_standalone_stream_test.go's live fixture for that).
package api

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// startEverythingServer launches the real @modelcontextprotocol/server-everything
// package over its Streamable HTTP transport on a free local port, and waits
// for it to accept connections before returning. Skips the test entirely if
// npx isn't available, rather than failing.
func startEverythingServer(t *testing.T) (baseURL string) {
	t.Helper()
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not found in PATH; skipping real server-everything e2e test")
	}

	port := freePort(t)
	cmd := exec.Command("npx", "-y", "@modelcontextprotocol/server-everything", "streamableHttp")
	cmd.Env = append(cmd.Environ(), fmt.Sprintf("PORT=%d", port))
	configureExternalProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server-everything: %v", err)
	}
	t.Cleanup(func() {
		// npx execs the actual server binary as a child process; killing just
		// the npx process it directly spawned would leak that child (the same
		// process-group leak internal/mcp's stdio handling guards against).
		killExternalProcessGroup(cmd.Process.Pid)
		_ = cmd.Wait()
	})

	baseURL = fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	if !pollUntil(t, 30*time.Second, 200*time.Millisecond, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}) {
		t.Fatalf("server-everything did not start listening on port within 30s (npx may need network access to fetch the package on first run)")
	}
	return baseURL
}

func TestMCPToolsCallAgainstRealEverythingServer(t *testing.T) {
	baseURL := startEverythingServer(t)

	agentServer, _ := newTestAgentServer(t, "everything", baseURL, 30, true)

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"trigger-long-running-operation","arguments":{"duration":3,"steps":3},"_meta":{"progressToken":"real-e2e-token"}}}`
	req, err := http.NewRequest(http.MethodPost, agentServer.URL+"/mcp/everything", strings.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("agent request: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}

	reader := bufio.NewReader(resp.Body)
	var progressTimes []time.Time
	var terminal sseEventFrame
	var terminalTime time.Time
	for {
		frame := readNextSSEFrame(t, reader)
		if strings.Contains(frame.data, "notifications/progress") {
			progressTimes = append(progressTimes, time.Now())
			if !strings.Contains(frame.data, "real-e2e-token") {
				t.Fatalf("expected the agent's own progressToken restored, got %q", frame.data)
			}
			continue
		}
		terminal = frame
		terminalTime = time.Now()
		break
	}

	if len(progressTimes) != 3 {
		t.Fatalf("expected 3 live progress notifications from the real everything server, got %d", len(progressTimes))
	}
	if !strings.Contains(terminal.data, "Long running operation completed") {
		t.Fatalf("expected the real terminal result, got %q", terminal.data)
	}

	// The whole point: the agent must see the first update well before the
	// terminal response could exist (duration=3s/steps=3 means the server
	// hasn't even finished sleeping when the first update arrives). If Atryum
	// buffered the whole response and replayed it at the end, every frame
	// would land within milliseconds of each other instead of spread ~1s apart.
	if gap := terminalTime.Sub(progressTimes[0]); gap < 1500*time.Millisecond {
		t.Fatalf("expected several seconds between the first live update and the terminal result (proving live delivery, not buffering), got %s", gap)
	}
	for i := 1; i < len(progressTimes); i++ {
		if gap := progressTimes[i].Sub(progressTimes[i-1]); gap < 500*time.Millisecond {
			t.Fatalf("expected a real ~1s gap between progress notification %d and %d, got %s", i, i+1, gap)
		}
	}
}
