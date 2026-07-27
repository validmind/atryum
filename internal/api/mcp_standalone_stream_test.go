//go:build mcpstandalone

// Real end-to-end test against a real MCP Python SDK (FastMCP) server run
// via uv, through a real running Atryum handler stack. Requires uv
// (https://docs.astral.sh/uv/) on PATH; uv resolves the `mcp` package into
// an ephemeral environment on first run, which needs network access.
// Excluded from `go test ./...` via this build tag so the default suite
// stays fast and hermetic. Run explicitly:
//
//	just mcp-standalone-stream-test
//
// or:
//
//	go test -tags mcpstandalone ./internal/api -run TestMCPToolsCallAgainstRealStandaloneStreamServer -v
//
// FastMCP's Context.report_progress doesn't attribute its notification to
// the request that triggered it (no related_request_id), so the server
// routes every progress update to the standalone SSE stream — never to the
// tools/call POST response body. This is the complement to
// mcp_everything_test.go, which proves the opposite case (progress on the
// same connection as the call). See internal/api/testdata/mcp_standalone_fixture/server.py
// for the server, and internal/mcp/client_test.go's
// TestInvokeStreamStandaloneStream* tests for the hermetic, hand-fixtured
// version of this same code path.
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

// startStandaloneFixtureServer launches the real FastMCP-based fixture
// server via `uv run --with mcp python3 ...` on a free local port, and waits
// for it to accept connections before returning. Skips the test entirely if
// uv isn't available, rather than failing.
func startStandaloneFixtureServer(t *testing.T) (baseURL string) {
	t.Helper()
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not found in PATH; skipping real standalone-stream e2e test")
	}

	port := freePort(t)
	cmd := exec.Command("uv", "run", "--with", "mcp", "python3", "testdata/mcp_standalone_fixture/server.py")
	cmd.Env = append(cmd.Environ(), fmt.Sprintf("PORT=%d", port))
	configureExternalProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start standalone fixture server: %v", err)
	}
	t.Cleanup(func() {
		// uv run may spawn python as a child process rather than exec'ing into
		// it directly; killing just the uv process it directly spawned could
		// leak that child (the same process-group leak internal/mcp's stdio
		// handling guards against).
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
		t.Fatalf("standalone fixture server did not start listening on port within 30s (uv may need network access to resolve the mcp package on first run)")
	}
	return baseURL
}

// TestMCPToolsCallAgainstRealStandaloneStreamServer is the standalone-stream
// counterpart to mcp_everything_test.go's TestMCPToolsCallAgainstRealEverythingServer:
// the same wall-clock-gap proof of live delivery, but every progress
// notification here arrives on the standalone GET stream rather than the
// tools/call POST response, since FastMCP's report_progress never attributes
// progress to a related_request_id:
//
//	t=0     POST tools/call slow_streaming_task
//	          steps=3, delay_seconds=1, progressToken=standalone-e2e-token
//	t=~1s   notifications/progress (via standalone GET)  -> progressTimes[0]
//	t=~2s   notifications/progress (via standalone GET)  -> progressTimes[1]
//	t=~3s   notifications/progress (via standalone GET)  -> progressTimes[2]
//	t=~3s+  terminal "done after 3 real progress notifications" -> terminalTime
//
// Same two assertions as the everything-server test: terminalTime minus
// progressTimes[0] >= 1500ms, and each consecutive gap >= 500ms. Reading only
// the tools/call POST response (the pre-fix behavior) would show 0 progress
// notifications here — see the comment above the read loop below.
func TestMCPToolsCallAgainstRealStandaloneStreamServer(t *testing.T) {
	baseURL := startStandaloneFixtureServer(t)

	agentServer, _ := newTestAgentServer(t, "standalone-fixture", baseURL, 30, true)

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow_streaming_task","arguments":{"steps":3,"delay_seconds":1},"_meta":{"progressToken":"standalone-e2e-token"}}}`
	req, err := http.NewRequest(http.MethodPost, agentServer.URL+"/mcp/standalone-fixture", strings.NewReader(reqBody))
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
			if !strings.Contains(frame.data, "standalone-e2e-token") {
				t.Fatalf("expected the agent's own progressToken restored, got %q", frame.data)
			}
			continue
		}
		terminal = frame
		terminalTime = time.Now()
		break
	}

	// If Atryum only read the tools/call POST response (the pre-fix
	// behavior), this would be 0: FastMCP's report_progress sends every
	// update exclusively on the standalone stream, never here.
	if len(progressTimes) != 3 {
		t.Fatalf("expected 3 live progress notifications relayed from the real standalone stream, got %d", len(progressTimes))
	}
	if !strings.Contains(terminal.data, "done after 3 real progress notifications") {
		t.Fatalf("expected the real terminal result, got %q", terminal.data)
	}

	if gap := terminalTime.Sub(progressTimes[0]); gap < 1500*time.Millisecond {
		t.Fatalf("expected several seconds between the first live update and the terminal result (proving live delivery, not buffering), got %s", gap)
	}
	for i := 1; i < len(progressTimes); i++ {
		if gap := progressTimes[i].Sub(progressTimes[i-1]); gap < 500*time.Millisecond {
			t.Fatalf("expected a real ~1s gap between progress notification %d and %d, got %s", i, i+1, gap)
		}
	}
}
