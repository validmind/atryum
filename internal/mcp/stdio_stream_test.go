//go:build !windows

package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFakeStdioServer creates a one-call MCP subprocess for the stdio tests.
func writeFakeStdioServer(t *testing.T, toolsCallScript string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-mcp.sh")
	content := "#!/usr/bin/env bash\n" +
		"set -euo pipefail\n" +
		"while IFS= read -r line; do\n" +
		"  if [[ -z \"$line\" ]]; then continue; fi\n" +
		"  if [[ \"$line\" == *'\"method\":\"initialize\"'* ]]; then\n" +
		"    id=$(echo \"$line\" | grep -o '\"id\":[0-9]*' | head -1 | cut -d: -f2)\n" +
		"    printf '%s\\n' \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"id\\\":${id},\\\"result\\\":{\\\"serverInfo\\\":{\\\"name\\\":\\\"fake\\\",\\\"version\\\":\\\"0.1.0\\\"},\\\"capabilities\\\":{}}}\"\n" +
		"  elif [[ \"$line\" == *'\"method\":\"notifications/initialized\"'* ]]; then\n" +
		"    continue\n" +
		"  elif [[ \"$line\" == *'\"method\":\"tools/call\"'* ]]; then\n" +
		toolsCallScript +
		"    exit 0\n" +
		"  fi\n" +
		"done\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInvokeStreamStdioRelaysEventsBeforeTerminalResponseExists(t *testing.T) {
	releaseFile := filepath.Join(t.TempDir(), "release")
	script := writeFakeStdioServer(t, ""+
		"    id=$(echo \"$line\" | grep -o '\"id\":[0-9]*' | head -1 | cut -d: -f2)\n"+
		"    printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}'\n"+
		"    while [ ! -f \"$RELEASE_FILE\" ]; do sleep 0.02; done\n"+
		"    printf '%s\\n' \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"id\\\":${id},\\\"result\\\":{\\\"content\\\":[{\\\"type\\\":\\\"text\\\",\\\"text\\\":\\\"done\\\"}]}}\"\n",
	)

	client := NewHTTPClient()
	upstream := Upstream{Name: "fake-stdio", Mode: UpstreamModeStdio, Command: script, Env: map[string]string{"RELEASE_FILE": releaseFile}}
	sink := &fakeStreamSink{onEvent: func(StreamEvent) error {
		// The subprocess is blocked in its own `while [ ! -f ... ]` loop and
		// cannot write the terminal response until this file exists — it
		// only gets created here, inside the callback fired once the client
		// has actually delivered the notification to the sink.
		return os.WriteFile(releaseFile, []byte("go"), 0o644)
	}}

	result, err := client.InvokeStream(context.Background(), upstream, "demo", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if !sink.started {
		t.Fatal("expected StreamStarted to fire")
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected exactly one relayed event, got %d", len(sink.events))
	}
	if !strings.Contains(string(sink.events[0].Data), "notifications/progress") {
		t.Fatalf("expected progress notification relayed, got %s", sink.events[0].Data)
	}
	if !strings.Contains(string(result.Body), "done") {
		t.Fatalf("expected terminal result body, got %s", result.Body)
	}
}

func TestInvokeStreamStdioTerminalOnlyResponseNeverTouchesSink(t *testing.T) {
	script := writeFakeStdioServer(t, ""+
		"    id=$(echo \"$line\" | grep -o '\"id\":[0-9]*' | head -1 | cut -d: -f2)\n"+
		"    printf '%s\\n' \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"id\\\":${id},\\\"result\\\":{\\\"content\\\":[{\\\"type\\\":\\\"text\\\",\\\"text\\\":\\\"ok\\\"}]}}\"\n",
	)

	client := NewHTTPClient()
	upstream := Upstream{Name: "fake-stdio", Mode: UpstreamModeStdio, Command: script}
	sink := &fakeStreamSink{}

	result, err := client.InvokeStream(context.Background(), upstream, "demo", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if sink.started || len(sink.events) != 0 {
		t.Fatalf("expected sink to never be touched when the upstream emits nothing but its terminal response, got started=%t events=%#v", sink.started, sink.events)
	}
	if !strings.Contains(string(result.Body), `"text":"ok"`) {
		t.Fatalf("expected terminal result body, got %s", result.Body)
	}
}

func TestInvokeStreamStdioRejectsOversizedMessage(t *testing.T) {
	script := writeFakeStdioServer(t, ""+
		"    printf '%0200d\\n' 0\n",
	)

	client := NewHTTPClient()
	upstream := Upstream{Name: "fake-stdio", Mode: UpstreamModeStdio, Command: script}
	_, err := client.InvokeStream(
		context.Background(),
		upstream,
		"demo",
		map[string]any{},
		nil,
		nil,
		&fakeStreamSink{},
		StreamOptions{MaxMessageBytes: 128},
	)
	if !errors.Is(err, ErrStreamMessageTooLarge) {
		t.Fatalf("InvokeStream error = %v, want ErrStreamMessageTooLarge", err)
	}
}

func TestInvokeStreamStdioTerminalErrorAfterNotification(t *testing.T) {
	script := writeFakeStdioServer(t, ""+
		"    printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}'\n"+
		"    id=$(echo \"$line\" | grep -o '\"id\":[0-9]*' | head -1 | cut -d: -f2)\n"+
		"    printf '%s\\n' \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"id\\\":${id},\\\"error\\\":{\\\"code\\\":-32000,\\\"message\\\":\\\"tool exploded\\\"}}\"\n",
	)

	client := NewHTTPClient()
	upstream := Upstream{Name: "fake-stdio", Mode: UpstreamModeStdio, Command: script}
	sink := &fakeStreamSink{}

	result, err := client.InvokeStream(context.Background(), upstream, "demo", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if !result.Failed {
		t.Fatalf("expected Failed result, got %#v", result)
	}
	if !strings.Contains(string(result.Body), "tool exploded") {
		t.Fatalf("expected error body, got %s", result.Body)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected the progress notification to have been relayed before the terminal error, got %d", len(sink.events))
	}
}

func TestInvokeStreamStdioIdleTimeoutAbortsRead(t *testing.T) {
	script := writeFakeStdioServer(t, ""+
		"    printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}'\n"+
		"    sleep 30\n", // never sends the terminal event; killed by the idle timeout well before this returns
	)

	client := NewHTTPClient()
	upstream := Upstream{Name: "fake-stdio", Mode: UpstreamModeStdio, Command: script}
	sink := &fakeStreamSink{}

	start := time.Now()
	_, err := client.InvokeStream(context.Background(), upstream, "demo", map[string]any{}, nil, nil, sink, StreamOptions{IdleTimeout: 50 * time.Millisecond})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an idle timeout error")
	}
	if !errors.Is(err, ErrStreamTimeout) {
		t.Fatalf("expected errors.Is(err, ErrStreamTimeout) to hold, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("idle timeout took too long to abort: %s", elapsed)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected exactly one relayed event before the timeout, got %d", len(sink.events))
	}
}

// TestInvokeStreamStdioSinkErrorDoesNotDeadlockOnPersistentServer is a
// regression test for the cleanup-defer ordering: when the sink aborts the
// relay (agent disconnected) with no timeout having fired, the cleanup
// defer runs cmd.Wait() — and a stdio server that keeps running (its outer
// read loop is stuck inside an inner emit loop, so it never notices
// stdin-close) would block Wait forever unless guard.stop() cancels the
// context (killing the process group) BEFORE the Wait. With separate
// defers in the natural order, LIFO ran Wait first — a deadlock this test
// would catch by hanging.
func TestInvokeStreamStdioSinkErrorDoesNotDeadlockOnPersistentServer(t *testing.T) {
	script := writeFakeStdioServer(t, ""+
		"    while true; do\n"+
		"      printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}'\n"+
		"      sleep 0.05\n"+
		"    done\n",
	)

	client := NewHTTPClient()
	upstream := Upstream{Name: "fake-stdio", Mode: UpstreamModeStdio, Command: script}
	sink := &fakeStreamSink{onEvent: func(StreamEvent) error {
		return errors.New("downstream connection closed")
	}}

	start := time.Now()
	_, err := client.InvokeStream(context.Background(), upstream, "demo", map[string]any{}, nil, nil, sink, StreamOptions{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the sink's error to be returned")
	}
	if !strings.Contains(err.Error(), "downstream connection closed") {
		t.Fatalf("expected the sink's error, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("expected a prompt return after the sink aborted (process-group kill before Wait), took %s", elapsed)
	}
}

// TestInvokeStreamStdioHandshakeHangBoundedByHeaderTimeout is a regression
// test: the stdio initialize handshake happens before body timeouts are
// armed, so it needs the header-phase bound — without it, a subprocess
// that starts but never answers initialize blocks the call with no bound
// of its own.
func TestInvokeStreamStdioHandshakeHangBoundedByHeaderTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hang-mcp.sh")
	content := "#!/usr/bin/env bash\n" +
		"set -euo pipefail\n" +
		"while IFS= read -r line; do\n" +
		"  if [[ \"$line\" == *'\"method\":\"initialize\"'* ]]; then\n" +
		"    sleep 30\n" + // never answers initialize; killed by the header timeout
		"  fi\n" +
		"done\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	client := NewHTTPClient()
	upstream := Upstream{Name: "fake-stdio", Mode: UpstreamModeStdio, Command: path}
	sink := &fakeStreamSink{}

	start := time.Now()
	_, err := client.InvokeStream(context.Background(), upstream, "demo", map[string]any{}, nil, nil, sink, StreamOptions{HeaderTimeout: 50 * time.Millisecond})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a handshake timeout error")
	}
	if !errors.Is(err, ErrStreamTimeout) {
		t.Fatalf("expected errors.Is(err, ErrStreamTimeout) for the hung handshake, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("handshake timeout took too long to abort: %s", elapsed)
	}
	if sink.started || len(sink.events) != 0 {
		t.Fatalf("expected the sink never to be touched during a failed handshake, got started=%t events=%d", sink.started, len(sink.events))
	}
}

// TestInvokeStdioSkipsStrayServerRequestBeforeTerminalResponse is a
// regression test for the readRPC correctness fix: a "first message with
// any id/result/error wins" reader would misinterpret a stray incoming
// server-to-client request (it has an id, but no result/error — readRPC's
// old check only looked for "any of id/result/error present") as the
// answer to our own call, before the real response ever arrives. This uses
// the plain buffered Invoke (not InvokeStream) since the fix applies
// there too.
func TestInvokeStdioSkipsStrayServerRequestBeforeTerminalResponse(t *testing.T) {
	script := writeFakeStdioServer(t, ""+
		"    printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":\"srv-1\",\"method\":\"sampling/createMessage\",\"params\":{}}'\n"+
		"    id=$(echo \"$line\" | grep -o '\"id\":[0-9]*' | head -1 | cut -d: -f2)\n"+
		"    printf '%s\\n' \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"id\\\":${id},\\\"result\\\":{\\\"content\\\":[{\\\"type\\\":\\\"text\\\",\\\"text\\\":\\\"real answer\\\"}]}}\"\n",
	)

	client := NewHTTPClient()
	upstream := Upstream{Name: "fake-stdio", Mode: UpstreamModeStdio, Command: script}

	result, err := client.Invoke(context.Background(), upstream, "demo", map[string]any{}, nil, nil)
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if !strings.Contains(string(result.Body), "real answer") {
		t.Fatalf("expected the real terminal response (readRPC must skip the stray server-to-client request), got %s", result.Body)
	}
}
