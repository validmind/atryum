package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/validmind/atryum/internal/version"
)

// invokeStdioStream is invokeStdio's live-relay counterpart. Stdio has no
// Content-Type or other signal that selects streaming, so StreamStarted fires
// only before the first intermediate notification or server request. A
// terminal-only response never touches the sink.
func (c *Client) invokeStdioStream(ctx context.Context, upstream Upstream, tool string, input map[string]any, requestID *string, meta map[string]any, sink StreamSink, opts StreamOptions) (InvokeResult, error) {
	if upstream.Command == "" {
		return InvokeResult{}, fmt.Errorf("stdio upstream %q missing command", upstream.Name)
	}
	guard := newCallTimeoutGuard(ctx)
	defer guard.stop()

	cmd := exec.CommandContext(guard.ctx, upstream.Command, upstream.Args...)
	cmd.Env = os.Environ()
	for k, v := range upstream.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	configureStdioProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return InvokeResult{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return InvokeResult{}, err
	}
	stderr := newBoundedBuffer(stdioStderrCap)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return InvokeResult{}, err
	}
	defer func() {
		// guard.stop() MUST run before cmd.Wait(): stopping cancels the
		// guard context, which triggers the process-group kill, which is
		// what makes Wait return. Deferring these separately would run
		// them in LIFO order — Wait before stop — and a stdio server that
		// keeps running after answering (normal for long-lived servers)
		// or that ignores stdin-close would then block Wait forever on
		// every return path where no timeout had fired (sink error, or a
		// successfully received terminal response). The process is
		// per-call and disposable, so killing it once we have our answer
		// (or have given up) is the correct lifecycle.
		guard.stop()
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	reader := bufio.NewReader(stdout)
	// The initialize handshake gets the same header-phase bound the HTTP
	// path applies before its response headers arrive: without it, a
	// subprocess that starts but never answers initialize would block
	// readRPC with no bound of its own (only the caller's ctx).
	guard.armSetupTimeout(opts.HeaderTimeout)
	initID := c.nextRPCID()
	if err := writeRPC(stdin, initID, "initialize", map[string]any{
		"protocolVersion": DefaultMCPProtocolVersion,
		"clientInfo":      map[string]any{"name": "atryum", "version": version.Version},
		"capabilities":    map[string]any{},
	}); err != nil {
		return InvokeResult{}, err
	}
	if _, err := readRPC(reader, rpcIDMessage(initID)); err != nil {
		if reason := guard.reason(); reason != "" {
			return InvokeResult{}, fmt.Errorf("upstream %q: %s during stdio initialize: %w", upstream.Name, reason, ErrStreamTimeout)
		}
		if stderr.Len() > 0 {
			return InvokeResult{}, fmt.Errorf("stdio upstream error: %s", strings.TrimSpace(stderr.String()))
		}
		return InvokeResult{}, err
	}
	guard.disarmSetupTimeout()
	_ = writeRPC(stdin, c.nextRPCID(), "notifications/initialized", map[string]any{})
	callParams := map[string]any{"name": tool, "arguments": input}
	if merged := mergeRequestMeta(meta, requestID); merged != nil {
		callParams["_meta"] = merged
	}
	callID := c.nextRPCID()
	if err := writeRPC(stdin, callID, "tools/call", callParams); err != nil {
		return InvokeResult{}, err
	}

	guard.armBodyTimeouts(opts.IdleTimeout, opts.MaxDuration)
	return c.relayStdioToolCall(reader, sink, guard, upstream, callID, stderr)
}

// relayStdioToolCall reads reader's newline-delimited JSON-RPC messages,
// relaying every intermediate (non-terminal) message to sink as it arrives,
// and returns once the terminal response for callID is read.
func (c *Client) relayStdioToolCall(reader *bufio.Reader, sink StreamSink, guard *callTimeoutGuard, upstream Upstream, callID int64, stderr *boundedBuffer) (InvokeResult, error) {
	expectedID := rpcIDMessage(callID)
	started := false
	ensureStarted := func() {
		if !started {
			started = true
			sink.StreamStarted()
		}
	}
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if reason := guard.reason(); reason != "" {
				return InvokeResult{}, fmt.Errorf("upstream %q %s: %w", upstream.Name, reason, ErrStreamTimeout)
			}
			if stderr.Len() > 0 {
				return InvokeResult{}, fmt.Errorf("stdio upstream error: %s", strings.TrimSpace(stderr.String()))
			}
			return InvokeResult{}, err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		guard.resetIdle()

		switch classifyRPCMessage(line, expectedID) {
		case rpcMessageTerminalResponse:
			var resp rpcResponse
			if err := json.Unmarshal(line, &resp); err != nil {
				continue
			}
			if len(resp.Error) > 0 && string(resp.Error) != "null" {
				return InvokeResult{StatusCode: http.StatusBadGateway, Body: resp.Error, Failed: true}, nil
			}
			body := resp.Result
			if len(body) == 0 {
				body = []byte(`{"ok":true}`)
			}
			return InvokeResult{StatusCode: http.StatusOK, Body: body, Failed: looksLikeToolError(body)}, nil
		case rpcMessageServerRequest:
			ensureStarted()
			if err := sink.Event(StreamEvent{Data: line, ServerRequest: true}); err != nil {
				return InvokeResult{}, err
			}
		case rpcMessageNotification:
			ensureStarted()
			if err := sink.Event(StreamEvent{Data: line}); err != nil {
				return InvokeResult{}, err
			}
		default:
			// Unparseable line, or a response to some other id. Not ours
			// to interpret; ignore and keep reading.
		}
	}
}
