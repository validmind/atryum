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

func (c *httpClient) ListAgents(ctx context.Context) ([]AgentInfo, error) {
	values := url.Values{}
	values.Set("limit", "100")
	var agents []AgentInfo
	for {
		endpoint := c.base + "/v1/agents"
		if encoded := values.Encode(); encoded != "" {
			endpoint += "?" + encoded
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		c.setHeaders(req)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return nil, fmt.Errorf("list agents: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var payload struct {
			Data     []json.RawMessage `json:"data"`
			NextPage string            `json:"next_page"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("list agents: decode: %w", err)
		}
		resp.Body.Close()
		for _, raw := range payload.Data {
			if agent, ok := parseAgentInfo(raw); ok {
				agents = append(agents, agent)
			}
		}
		if payload.NextPage == "" {
			return agents, nil
		}
		values.Set("page", payload.NextPage)
	}
}

func (c *httpClient) GetAgent(ctx context.Context, agentID string) (AgentInfo, error) {
	endpoint := fmt.Sprintf("%s/v1/agents/%s", c.base, url.PathEscape(agentID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return AgentInfo{}, err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return AgentInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return AgentInfo{}, fmt.Errorf("get agent: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return AgentInfo{}, err
	}
	agent, ok := parseAgentInfo(raw)
	if !ok {
		return AgentInfo{}, fmt.Errorf("get agent: invalid response")
	}
	return agent, nil
}

func (c *httpClient) UpdateAgentMetadata(ctx context.Context, agentID string, version int, metadata map[string]*string) (AgentInfo, error) {
	body, err := json.Marshal(map[string]any{"version": version, "metadata": metadata})
	if err != nil {
		return AgentInfo{}, err
	}
	endpoint := fmt.Sprintf("%s/v1/agents/%s", c.base, url.PathEscape(agentID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return AgentInfo{}, err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return AgentInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return AgentInfo{}, fmt.Errorf("update agent metadata: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return AgentInfo{}, err
	}
	agent, ok := parseAgentInfo(raw)
	if !ok {
		return AgentInfo{}, fmt.Errorf("update agent metadata: invalid response")
	}
	return agent, nil
}

func (c *httpClient) ListSessions(ctx context.Context, filter SessionListFilter) ([]SessionInfo, error) {
	values := url.Values{}
	if filter.AgentID != "" {
		values.Set("agent_id", filter.AgentID)
	}
	values.Set("limit", "100")
	values.Set("order", "desc")
	var sessions []SessionInfo
	for {
		endpoint := c.base + "/v1/sessions"
		if encoded := values.Encode(); encoded != "" {
			endpoint += "?" + encoded
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		c.setHeaders(req)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return nil, fmt.Errorf("list sessions: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var payload struct {
			Data     []json.RawMessage `json:"data"`
			NextPage string            `json:"next_page"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("list sessions: decode: %w", err)
		}
		resp.Body.Close()
		for _, raw := range payload.Data {
			if session, ok := parseSessionInfo(raw); ok {
				sessions = append(sessions, session)
			}
		}
		if payload.NextPage == "" {
			return sessions, nil
		}
		values.Set("page", payload.NextPage)
	}
}

func parseAgentInfo(raw json.RawMessage) (AgentInfo, bool) {
	var env struct {
		ID          string          `json:"id"`
		Name        string          `json:"name"`
		Description *string         `json:"description"`
		Model       json.RawMessage `json:"model"`
		Metadata    json.RawMessage `json:"metadata"`
		Version     int             `json:"version"`
		CreatedAt   string          `json:"created_at"`
		UpdatedAt   string          `json:"updated_at"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.ID == "" {
		return AgentInfo{}, false
	}
	agent := AgentInfo{ID: env.ID, Name: env.Name, Version: env.Version, Model: parseAgentModel(env.Model), Metadata: parseMetadata(env.Metadata)}
	if env.Description != nil {
		agent.Description = *env.Description
	}
	if env.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, env.CreatedAt); err == nil {
			agent.CreatedAt = t.UTC()
		}
	}
	if env.UpdatedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, env.UpdatedAt); err == nil {
			agent.UpdatedAt = t.UTC()
		}
	}
	return agent, true
}

func parseMetadata(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 || string(raw) == "null" {
		return out
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return out
	}
	for key, value := range values {
		switch v := value.(type) {
		case string:
			out[key] = v
		case bool:
			if v {
				out[key] = "true"
			} else {
				out[key] = "false"
			}
		case float64:
			out[key] = fmt.Sprintf("%g", v)
		case nil:
			// Deleted/empty values are ignored.
		default:
			b, _ := json.Marshal(v)
			out[key] = string(b)
		}
	}
	return out
}

func parseAgentModel(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.ID
	}
	return ""
}

func parseSessionInfo(raw json.RawMessage) (SessionInfo, bool) {
	var env struct {
		ID        string          `json:"id"`
		Agent     json.RawMessage `json:"agent"`
		AgentID   string          `json:"agent_id"`
		Title     string          `json:"title"`
		Status    string          `json:"status"`
		CreatedAt string          `json:"created_at"`
		UpdatedAt string          `json:"updated_at"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.ID == "" {
		return SessionInfo{}, false
	}
	session := SessionInfo{ID: env.ID, AgentID: env.AgentID, Title: env.Title, Status: env.Status}
	if session.AgentID == "" {
		session.AgentID = parseSessionAgentID(env.Agent)
	}
	if env.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, env.CreatedAt); err == nil {
			session.CreatedAt = t.UTC()
		}
	}
	if env.UpdatedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, env.UpdatedAt); err == nil {
			session.UpdatedAt = t.UTC()
		}
	}
	return session, true
}

func parseSessionAgentID(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.ID
	}
	return ""
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
	values := url.Values{}
	values.Set("limit", "100")
	values.Set("order", "asc")
	var events []RawEvent
	for {
		endpoint := fmt.Sprintf("%s/v1/sessions/%s/events", c.base, url.PathEscape(sessionID))
		if encoded := values.Encode(); encoded != "" {
			endpoint += "?" + encoded
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		c.setHeaders(req)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return nil, fmt.Errorf("list events: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var payload struct {
			Data     []json.RawMessage `json:"data"`
			NextPage string            `json:"next_page"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("list events: decode: %w", err)
		}
		resp.Body.Close()
		for _, raw := range payload.Data {
			events = append(events, parseEnvelope(raw))
		}
		if payload.NextPage == "" {
			break
		}
		values.Set("page", payload.NextPage)
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
