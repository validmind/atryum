package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// sseEventReader incrementally parses a Server-Sent Events body, returning
// one joined "data:" payload per event via Next. It is the shared parser
// behind both the buffered SSE consumers (extractSSEJSONRPCResponse, used by
// tools/list, initialize, and the default forward path) and the incremental
// streaming relay (relaySSEToolCall) — one parser, two ways of consuming it.
type sseEventReader struct {
	scanner   *bufio.Scanner
	dataLines []string
	eventID   string
	retry     time.Duration
	hasData   bool
	hasID     bool
	hasRetry  bool
}

type sseWireEvent struct {
	Data     []byte
	ID       string
	Retry    time.Duration
	HasData  bool
	HasID    bool
	HasRetry bool
}

func newSSEEventReader(r io.Reader) *sseEventReader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	return &sseEventReader{scanner: scanner}
}

// NextEvent returns one complete SSE event, including the id/retry fields
// needed to resume a Streamable HTTP response after the upstream closes it.
func (r *sseEventReader) NextEvent() (sseWireEvent, error) {
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if line == "" {
			if !r.hasData && !r.hasID && !r.hasRetry {
				continue
			}
			return r.takeEvent(), nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			field = line
			value = ""
		} else if strings.HasPrefix(value, " ") {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "data":
			r.dataLines = append(r.dataLines, value)
			r.hasData = true
		case "id":
			// The SSE specification ignores id values containing NUL.
			if !strings.ContainsRune(value, '\x00') {
				r.eventID = value
				r.hasID = true
			}
		case "retry":
			millis, err := strconv.ParseInt(value, 10, 64)
			if err == nil && millis >= 0 {
				const maxRetryMillis = int64((time.Duration(1<<63 - 1)) / time.Millisecond)
				if millis > maxRetryMillis {
					r.retry = time.Duration(1<<63 - 1)
				} else {
					r.retry = time.Duration(millis) * time.Millisecond
				}
				r.hasRetry = true
			}
		}
	}
	if err := r.scanner.Err(); err != nil {
		return sseWireEvent{}, err
	}
	if r.hasData || r.hasID || r.hasRetry {
		return r.takeEvent(), nil
	}
	return sseWireEvent{}, io.EOF
}

func (r *sseEventReader) takeEvent() sseWireEvent {
	evt := sseWireEvent{
		Data:     []byte(strings.Join(r.dataLines, "\n")),
		ID:       r.eventID,
		Retry:    r.retry,
		HasData:  r.hasData,
		HasID:    r.hasID,
		HasRetry: r.hasRetry,
	}
	r.dataLines = nil
	r.eventID = ""
	r.retry = 0
	r.hasData = false
	r.hasID = false
	r.hasRetry = false
	return evt
}

// Next is the payload-only view used by buffered consumers. Control-only
// events (id/retry with no data) are skipped because they carry no JSON-RPC
// message for those callers to decode.
func (r *sseEventReader) Next() ([]byte, error) {
	for {
		evt, err := r.NextEvent()
		if err != nil {
			return nil, err
		}
		if evt.HasData {
			return evt.Data, nil
		}
	}
}

// extractSSEJSONRPCResponse scans an SSE body for the one event that is
// either the response matching expectedID or the null-id error JSON-RPC
// uses when a server can't identify which request an error belongs to,
// skipping everything else (notifications, unrelated responses).
func extractSSEJSONRPCResponse(r io.Reader, expectedID json.RawMessage) ([]byte, error) {
	reader := newSSEEventReader(r)
	for {
		payload, err := reader.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("no JSON-RPC response in SSE stream")
		}
		if err != nil {
			return nil, err
		}
		match := classifyJSONRPCResponsePayload(payload, expectedID)
		if match == jsonRPCResponseIDMatch || match == jsonRPCResponseNullIDError {
			return payload, nil
		}
	}
}
