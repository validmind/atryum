package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/validmind/atryum/internal/mcp"
)

// toolCallStream relays a proxied tools/call's intermediate upstream
// messages to the agent as they arrive. Until the first one, it passes
// writes straight through, so a call with none answers with plain JSON as
// before. The first message turns the response into text/event-stream: the
// headers are flushed and each message is written as its own SSE event.
// From then on the JSON-RPC response the handler writes is buffered and
// sent as the stream's last event by finish.
//
// Events carry no id field: Atryum does not offer resumption.
type toolCallStream struct {
	http.ResponseWriter
	flusher   http.Flusher
	streaming bool
	pending   bytes.Buffer
}

// newToolCallStream returns w wrapped for relaying, and a sink for the
// upstream client, or w itself and nil when w cannot flush.
func newToolCallStream(w http.ResponseWriter, params json.RawMessage) (http.ResponseWriter, *toolCallStream, *mcp.StreamSink) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return w, nil, nil
	}
	s := &toolCallStream{ResponseWriter: w, flusher: flusher}
	var meta struct {
		Meta struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(params, &meta)
	return s, s, &mcp.StreamSink{ProgressToken: meta.Meta.ProgressToken, Emit: s.emit}
}

func (s *toolCallStream) emit(evt mcp.StreamEvent) {
	if !s.streaming {
		s.streaming = true
		h := s.ResponseWriter.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		s.ResponseWriter.WriteHeader(http.StatusOK)
	}
	writeSSEEvent(s.ResponseWriter, evt.Event, evt.Message)
	s.flusher.Flush()
}

func (s *toolCallStream) WriteHeader(status int) {
	if !s.streaming {
		s.ResponseWriter.WriteHeader(status)
	}
}

func (s *toolCallStream) Write(b []byte) (int, error) {
	if !s.streaming {
		return s.ResponseWriter.Write(b)
	}
	return s.pending.Write(b)
}

func (s *toolCallStream) Flush() {
	if !s.streaming {
		s.flusher.Flush()
	}
}

// finish sends the buffered JSON-RPC response as the stream's last event.
func (s *toolCallStream) finish() {
	if s == nil || !s.streaming || s.pending.Len() == 0 {
		return
	}
	writeSSEEvent(s.ResponseWriter, "message", bytes.TrimSpace(s.pending.Bytes()))
	s.pending.Reset()
	s.flusher.Flush()
}

func writeSSEEvent(w http.ResponseWriter, name string, data []byte) {
	if name = strings.TrimSpace(name); name != "" {
		fmt.Fprintf(w, "event: %s\n", name)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprint(w, "\n")
}
