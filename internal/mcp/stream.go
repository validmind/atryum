package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// maxRecordedStreamEvents caps how many intermediate messages one tools/call
// keeps for its audit record. Every message is still relayed; the rest are
// only counted (InvokeResult.StreamDropped).
const maxRecordedStreamEvents = 256

// StreamEvent is one intermediate JSON-RPC message an upstream sent for a
// tools/call before its response: a progress or logging notification, or
// partial content.
type StreamEvent struct {
	// Event is the SSE event name the upstream used, empty for stdio and for
	// unnamed SSE events (which SSE treats as "message").
	Event      string          `json:"event,omitempty"`
	Method     string          `json:"method"`
	Message    json.RawMessage `json:"message"`
	ReceivedAt time.Time       `json:"received_at"`
}

// StreamSink receives a tools/call's intermediate messages as they arrive.
// Emit runs on the goroutine that called Invoke. ProgressToken is the
// downstream caller's _meta.progressToken; it is passed to the upstream
// unchanged so the progress notifications it sends back name a token the
// caller knows.
type StreamSink struct {
	ProgressToken json.RawMessage
	Emit          func(StreamEvent)
}

type streamSinkKey struct{}

// WithStreamSink returns ctx carrying sink. Invoke relays a tools/call's
// intermediate messages to it; without one they are only recorded.
func WithStreamSink(ctx context.Context, sink *StreamSink) context.Context {
	return context.WithValue(ctx, streamSinkKey{}, sink)
}

func streamSinkFromContext(ctx context.Context) *StreamSink {
	sink, _ := ctx.Value(streamSinkKey{}).(*StreamSink)
	return sink
}

// streamRecorder keeps a tools/call's intermediate messages for the audit
// record and hands each one to the sink, if any.
type streamRecorder struct {
	sink    *StreamSink
	events  []StreamEvent
	dropped int
}

func newStreamRecorder(ctx context.Context) *streamRecorder {
	return &streamRecorder{sink: streamSinkFromContext(ctx)}
}

// observe takes one non-terminal message. Only notifications are relayed: a
// request from the upstream (sampling, elicitation, roots) would be answered
// to Atryum, which cannot broker it, so it is dropped and reported false.
func (r *streamRecorder) observe(event string, payload []byte) bool {
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil || strings.TrimSpace(msg.Method) == "" || len(msg.ID) > 0 {
		return false
	}
	evt := StreamEvent{Event: event, Method: msg.Method, Message: append(json.RawMessage(nil), payload...), ReceivedAt: time.Now().UTC()}
	if len(r.events) < maxRecordedStreamEvents {
		r.events = append(r.events, evt)
	} else {
		r.dropped++
	}
	if r.sink != nil && r.sink.Emit != nil {
		r.sink.Emit(evt)
	}
	return true
}

// toolCallMeta is the _meta Atryum sends with an upstream tools/call.
func (r *streamRecorder) toolCallMeta(requestID *string) map[string]any {
	meta := map[string]any{}
	if requestID != nil && *requestID != "" {
		meta["atryumRequestId"] = *requestID
	}
	if r.sink != nil && len(r.sink.ProgressToken) > 0 {
		meta["progressToken"] = r.sink.ProgressToken
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

type sseEvent struct {
	Name string
	Data []byte
}

// scanSSE reads an SSE stream and calls fn for each event that carries data,
// as soon as its terminating blank line arrives, until fn returns true or the
// stream ends. Multi-line data fields are joined with "\n"; comments, id and
// retry fields are skipped.
func scanSSE(r io.Reader, fn func(sseEvent) bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	var name string
	var dataLines []string
	dispatch := func() bool {
		if len(dataLines) == 0 {
			name = ""
			return false
		}
		evt := sseEvent{Name: name, Data: []byte(strings.Join(dataLines, "\n"))}
		name, dataLines = "", nil
		return fn(evt)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if dispatch() {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			field = line
			value = ""
		} else {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "data":
			dataLines = append(dataLines, value)
		case "event":
			name = value
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	dispatch()
	return nil
}

// relaySSEJSONRPCResponse reads an upstream SSE response as it arrives,
// passing every event before the response matching expectedID to onEvent,
// and returns that response's payload. It stops reading at the response.
func relaySSEJSONRPCResponse(r io.Reader, expectedID json.RawMessage, onEvent func(sseEvent)) ([]byte, error) {
	var terminal []byte
	err := scanSSE(r, func(evt sseEvent) bool {
		match := classifyJSONRPCResponsePayload(evt.Data, expectedID)
		if match == jsonRPCResponseIDMatch || match == jsonRPCResponseNullIDError {
			terminal = evt.Data
			return true
		}
		if onEvent != nil {
			onEvent(evt)
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	if terminal == nil {
		return nil, fmt.Errorf("no JSON-RPC response in SSE stream")
	}
	return terminal, nil
}
