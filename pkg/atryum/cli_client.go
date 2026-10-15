package atryum

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/validmind/atryum/internal/api"
)

// atryumClient talks to one Atryum server's operator API on behalf of a
// logged-in user. token is consulted per request so refreshed credentials
// are picked up; it may return "" for no-auth deployments.
type atryumClient struct {
	baseURL string
	http    *http.Client
	token   func(ctx context.Context) (string, error)
}

// apiError is a non-2xx response from Atryum with the server's message.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("atryum returned %d", e.Status)
	}
	return fmt.Sprintf("atryum returned %d: %s", e.Status, e.Message)
}

func newAtryumClient(baseURL string, token func(ctx context.Context) (string, error)) *atryumClient {
	return &atryumClient{
		baseURL: normalizeServerURL(baseURL),
		http:    &http.Client{Timeout: 30 * time.Second},
		token:   token,
	}
}

func (c *atryumClient) do(ctx context.Context, method, path string, body any, into any) error {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != nil {
		tok, err := c.token(ctx)
		if err != nil {
			return err
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiError{Status: resp.StatusCode, Message: extractAPIErrorMessage(raw)}
	}
	if into == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("decode %s %s: %w", method, path, err)
	}
	return nil
}

func extractAPIErrorMessage(raw []byte) string {
	var envelope struct {
		Error            any    `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return strings.TrimSpace(string(raw))
	}
	switch v := envelope.Error.(type) {
	case string:
		if envelope.ErrorDescription != "" {
			return envelope.ErrorDescription
		}
		return v
	case map[string]any:
		if msg, ok := v["message"].(string); ok {
			return msg
		}
	}
	return strings.TrimSpace(string(raw))
}

// ─── Typed calls ─────────────────────────────────────────────────────────────

func (c *atryumClient) authConfig(ctx context.Context) (api.AuthConfigResponse, error) {
	var resp api.AuthConfigResponse
	// Public endpoint: never send a token so an expired one can't 401 here.
	plain := &atryumClient{baseURL: c.baseURL, http: c.http}
	err := plain.do(ctx, http.MethodGet, "/api/v1/auth/config", nil, &resp)
	return resp, err
}

func (c *atryumClient) me(ctx context.Context) (api.MeResponse, error) {
	var resp api.MeResponse
	err := c.do(ctx, http.MethodGet, "/api/v1/me", nil, &resp)
	return resp, err
}

func (c *atryumClient) listAgents(ctx context.Context) ([]api.OperatorAgent, error) {
	var resp api.AgentListResponse
	if err := c.do(ctx, http.MethodGet, "/api/v1/agents", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

func (c *atryumClient) createAgent(ctx context.Context, name, description string) (api.OperatorAgent, error) {
	var resp api.OperatorAgent
	err := c.do(ctx, http.MethodPost, "/api/v1/agents", map[string]any{
		"name": name, "description": description, "enabled": true,
	}, &resp)
	return resp, err
}

func (c *atryumClient) listKeys(ctx context.Context, agentID string) ([]api.OperatorAPIKey, error) {
	var resp api.APIKeyListResponse
	if err := c.do(ctx, http.MethodGet, "/api/v1/agents/"+agentID+"/keys", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

func (c *atryumClient) createKey(ctx context.Context, agentID, name, expiresIn string) (api.OperatorAPIKey, error) {
	var resp api.OperatorAPIKey
	body := map[string]any{"name": name}
	if expiresIn != "" {
		body["expires_in"] = expiresIn
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/agents/"+agentID+"/keys", body, &resp)
	return resp, err
}

func (c *atryumClient) revokeKey(ctx context.Context, agentID, keyID string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/agents/"+agentID+"/keys/"+keyID, nil, nil)
}

// resolveAgent finds an agent by id (cuid) or exact, then case-insensitive,
// name. It errors when the name is ambiguous.
func resolveAgent(agents []api.OperatorAgent, ref string) (api.OperatorAgent, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return api.OperatorAgent{}, errors.New("agent name or id is required")
	}
	for _, a := range agents {
		if a.CUID == ref {
			return a, nil
		}
	}
	var exact, loose []api.OperatorAgent
	for _, a := range agents {
		if a.Name == ref {
			exact = append(exact, a)
		} else if strings.EqualFold(a.Name, ref) {
			loose = append(loose, a)
		}
	}
	candidates := exact
	if len(candidates) == 0 {
		candidates = loose
	}
	switch len(candidates) {
	case 0:
		return api.OperatorAgent{}, fmt.Errorf("no agent named %q (run `atryum agent list`)", ref)
	case 1:
		return candidates[0], nil
	default:
		ids := make([]string, 0, len(candidates))
		for _, a := range candidates {
			ids = append(ids, a.CUID)
		}
		return api.OperatorAgent{}, fmt.Errorf("agent name %q is ambiguous; use one of the ids: %s", ref, strings.Join(ids, ", "))
	}
}
