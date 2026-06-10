package managedagents

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// httpClient is the concrete AnthropicClient backed by api.anthropic.com.
type httpClient struct {
	base   string
	apiKey string
	http   *http.Client
}

// NewAnthropicHTTPClient builds an AnthropicClient for the given config.
func NewAnthropicHTTPClient(cfg Config) AnthropicClient {
	cfg = cfg.withDefaults()
	return &httpClient{
		base:   strings.TrimRight(cfg.BaseURL, "/"),
		apiKey: cfg.APIKey,
		// No client-wide timeout: the SSE stream is long-lived. Per-request
		// timeouts are applied via context by callers.
		http: &http.Client{},
	}
}

func (c *httpClient) setHeaders(req *http.Request) {
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicVer)
	req.Header.Set("anthropic-beta", managedAgentsBeta)
	req.Header.Set("content-type", "application/json")
}

// rawEventEnvelope captures the fields the bridge keys on. The full JSON is
// retained separately so downstream handling has access to everything.
type rawEventEnvelope struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	ProcessedAt string `json:"processed_at"`
}

func parseEnvelope(raw json.RawMessage) RawEvent {
	evt, _ := parseEnvelopeStrict(raw)
	return evt
}

func parseEnvelopeStrict(raw json.RawMessage) (RawEvent, error) {
	var env rawEventEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return RawEvent{}, err
	}
	evt := RawEvent{ID: env.ID, Type: env.Type, Raw: raw}
	if env.ProcessedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, env.ProcessedAt); err == nil {
			evt.ProcessedAt = t.UTC()
		}
	}
	if evt.ProcessedAt.IsZero() {
		evt.ProcessedAt = time.Now().UTC()
	}
	return evt, nil
}

func (c *httpClient) ListEventsSince(ctx context.Context, sessionID, afterEventID string) ([]RawEvent, error) {
	endpoint := fmt.Sprintf("%s/v1/sessions/%s/events", c.base, url.PathEscape(sessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("list events: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("list events: decode: %w", err)
	}
	events := make([]RawEvent, 0, len(payload.Data))
	for _, raw := range payload.Data {
		events = append(events, parseEnvelope(raw))
	}
	// Drop everything up to and including the cursor so callers only see new
	// events. The API returns history oldest-first.
	if afterEventID != "" {
		idx := -1
		for i, e := range events {
			if e.ID == afterEventID {
				idx = i
				break
			}
		}
		if idx >= 0 {
			events = events[idx+1:]
		}
	}
	return events, nil
}

func (c *httpClient) StreamEvents(ctx context.Context, sessionID string) (EventStream, error) {
	endpoint := fmt.Sprintf("%s/v1/sessions/%s/events/stream", c.base, url.PathEscape(sessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	req.Header.Set("accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("stream events: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	scanner := bufio.NewScanner(resp.Body)
	// A 1MiB line buffer accommodates large tool inputs/results inlined in a
	// single SSE data frame.
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	return &sseStream{body: resp.Body, scanner: scanner}, nil
}

func (c *httpClient) SendEvents(ctx context.Context, sessionID string, events []OutboundEvent) error {
	endpoint := fmt.Sprintf("%s/v1/sessions/%s/events", c.base, url.PathEscape(sessionID))
	body, err := json.Marshal(map[string]any{"events": events})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("send events: status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// sseStream parses `data:` lines from an SSE response body into RawEvents.
type sseStream struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
}

func (s *sseStream) Next(ctx context.Context) (RawEvent, error) {
	var dataLines []string
	for {
		if err := ctx.Err(); err != nil {
			return RawEvent{}, err
		}
		if !s.scanner.Scan() {
			if err := s.scanner.Err(); err != nil {
				return RawEvent{}, err
			}
			if len(dataLines) > 0 {
				return parseSSEDataLines(dataLines)
			}
			return RawEvent{}, io.EOF
		}
		line := strings.TrimRight(s.scanner.Text(), "\r")
		if line == "" {
			if len(dataLines) == 0 {
				continue // blank separator with no message
			}
			evt, err := parseSSEDataLines(dataLines)
			if err != nil {
				return RawEvent{}, err
			}
			dataLines = nil
			return evt, nil
		}
		if strings.HasPrefix(line, ":") {
			continue // keep-alive ping
		}
		if !strings.HasPrefix(line, "data:") {
			continue // ignore event:/id: lines; the JSON carries its own type
		}
		data := strings.TrimPrefix(line, "data:")
		if strings.HasPrefix(data, " ") {
			data = data[1:]
		}
		dataLines = append(dataLines, data)
	}
}

func (s *sseStream) Close() error { return s.body.Close() }

func parseSSEDataLines(lines []string) (RawEvent, error) {
	data := strings.Join(lines, "\n")
	evt, err := parseEnvelopeStrict(json.RawMessage(data))
	if err != nil {
		return RawEvent{}, fmt.Errorf("stream events: decode SSE data: %w", err)
	}
	return evt, nil
}
