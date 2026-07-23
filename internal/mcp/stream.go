package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const defaultStreamMaxMessageBytes = 4 * 1024 * 1024

// ErrStreamMessageTooLarge marks an upstream response whose encoded JSON-RPC
// message exceeds StreamOptions.MaxMessageBytes.
var ErrStreamMessageTooLarge = errors.New("stream message exceeds configured byte limit")

// StreamEvent is one intermediate upstream JSON-RPC message, independent of
// whether HTTP SSE or stdio carried it. It is either a notification (progress,
// logging, or another server-to-client notification) or, more rarely, a
// server-to-client request.
type StreamEvent struct {
	// Data is one raw JSON-RPC message. HTTP SSE joins the event's data lines
	// with newlines; stdio removes its newline framing.
	Data []byte
	// ServerRequest is true when Data is a JSON-RPC request from the
	// upstream (has both id and method) rather than a notification. Atryum
	// does not broker server-initiated requests (sampling, elicitation,
	// roots); these are surfaced to the sink for audit only, never relayed
	// to the agent.
	ServerRequest bool
}

// StreamStats contains transport observations that are not themselves
// relayed JSON-RPC messages.
type StreamStats struct {
	StandaloneEventsDropped int64
}

// StreamStatsSink is an optional extension implemented by sinks that record
// transport-level statistics. HTTP calls using the standalone progress path
// call it once before return.
type StreamStatsSink interface {
	StreamStats(stats StreamStats)
}

// StreamSink receives intermediate upstream messages live, as InvokeStream
// reads them, so a caller can relay them onward (or just audit them) before
// the terminal response exists. Its methods run synchronously on the same
// goroutine as the InvokeStream call — there is no concurrent access to the
// sink, and no need for the sink to synchronize internally on that account.
type StreamSink interface {
	// StreamStarted fires at most once. HTTP SSE calls it before the first
	// event or terminal response; stdio calls it only before the first
	// intermediate event. A silently retried attempt never calls it.
	StreamStarted()
	// Event delivers one intermediate (non-terminal) message. A returned
	// error aborts the stream: InvokeStream stops reading and returns that
	// error to its caller.
	Event(evt StreamEvent) error
}

// StreamOptions bounds InvokeStream's setup and response-reading phases. A
// zero-valued field disables that particular bound.
type StreamOptions struct {
	// HeaderTimeout bounds setup before tool response reading begins: HTTP
	// session initialization and response headers, or the stdio initialize
	// handshake. Zero leaves setup bounded only by ctx's deadline, if any.
	HeaderTimeout time.Duration
	// IdleTimeout bounds response-reading inactivity. Streaming transports
	// reset it when upstream activity arrives, including events routed over
	// the shared standalone HTTP stream. For a plain HTTP JSON response it
	// bounds the complete body read. Zero disables the check.
	IdleTimeout time.Duration
	// MaxDuration bounds the complete response-reading phase after HTTP
	// headers or the stdio handshake. Zero disables the check.
	MaxDuration time.Duration
	// MaxMessageBytes bounds one decoded SSE event, one stdio JSON-RPC line,
	// or one plain JSON response body. Zero uses the safe 4 MiB default.
	MaxMessageBytes int
}

func (o StreamOptions) maxMessageBytes() int {
	if o.MaxMessageBytes > 0 {
		return o.MaxMessageBytes
	}
	return defaultStreamMaxMessageBytes
}

// InvokeStream behaves like Invoke while also relaying intermediate JSON-RPC
// messages to sink as they arrive. HTTP upstreams select streaming with an
// SSE response, so StreamStarted fires even when that response contains only
// its terminal message. Stdio starts the sink only when an intermediate
// message arrives. A nil sink always uses Invoke's buffered path.
func (c *Client) InvokeStream(ctx context.Context, upstream Upstream, tool string, input map[string]any, requestID *string, meta map[string]any, sink StreamSink, opts StreamOptions) (InvokeResult, error) {
	switch upstream.Mode {
	case UpstreamModeStdio:
		if sink == nil {
			return c.Invoke(ctx, upstream, tool, input, requestID, meta)
		}
		started := time.Now()
		defer func() {
			c.debugf("upstream invoke-stream transport=%s server=%s tool=%s duration_ms=%d", upstream.Mode, upstream.Name, tool, time.Since(started).Milliseconds())
		}()
		return c.invokeStdioStream(ctx, upstream, tool, input, requestID, meta, sink, opts)
	case UpstreamModeHTTP, "":
		if sink == nil {
			return c.Invoke(ctx, upstream, tool, input, requestID, meta)
		}
		started := time.Now()
		defer func() {
			c.debugf("upstream invoke-stream transport=%s server=%s tool=%s duration_ms=%d", upstream.Mode, upstream.Name, tool, time.Since(started).Milliseconds())
		}()
		return c.invokeHTTPStream(ctx, upstream, tool, input, requestID, meta, sink, opts)
	default:
		return InvokeResult{}, fmt.Errorf("unsupported upstream mode %q", upstream.Mode)
	}
}

type rpcMessageKind int

const (
	rpcMessageUnknown rpcMessageKind = iota
	rpcMessageTerminalResponse
	rpcMessageNotification
	rpcMessageServerRequest
)

// classifyRPCMessage identifies one already-parsed JSON-RPC message for the
// streaming relay: the terminal response to our request (matches
// expectedID, or is the null-id error the JSON-RPC spec uses when a server
// can't identify which request an error belongs to), a notification (no id),
// a server-to-client request (id and method, no result/error), or unknown
// (e.g. a response to some other id — not ours to interpret). Transport-
// neutral: used for both the HTTP SSE relay (relaySSEToolCall) and the
// stdio relay (relayStdioToolCall), and for stdio's non-streaming readRPC,
// since a JSON-RPC message's shape doesn't depend on how it was framed on
// the wire.
func classifyRPCMessage(payload []byte, expectedID json.RawMessage) rpcMessageKind {
	var message map[string]json.RawMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return rpcMessageUnknown
	}
	id, hasID := message["id"]
	_, hasMethod := message["method"]
	_, hasResult := message["result"]
	_, hasError := message["error"]

	if hasID && (hasResult || hasError) {
		if hasError && jsonRawIsNull(id) {
			return rpcMessageTerminalResponse
		}
		if jsonRPCIDsMatch(id, expectedID) {
			return rpcMessageTerminalResponse
		}
		return rpcMessageUnknown
	}
	if hasID && hasMethod {
		return rpcMessageServerRequest
	}
	if !hasID && hasMethod {
		return rpcMessageNotification
	}
	return rpcMessageUnknown
}
