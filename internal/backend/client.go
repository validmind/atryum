package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"atryum/internal/config"
)

const connectionPath = "/internal/v1/atryum/connection"
const agentsPath = "/internal/v1/atryum/agents"
const modelConfigsPath = "/internal/v1/atryum/model-configs"
const evaluatePath = "/internal/v1/atryum/evaluate"

type ConnectionResponse struct {
	OK              bool   `json:"ok"`
	MachineUserCUID string `json:"machine_user_cuid"`
	ServiceName     string `json:"service_name"`
}

// Agent represents an inventory model record returned by the backend.
type Agent struct {
	CUID         string         `json:"cuid"`
	Name         string         `json:"name"`
	CustomFields map[string]any `json:"custom_fields"`
}

type AgentsResponse struct {
	OrgCUID string  `json:"org_cuid"`
	OrgName string  `json:"org_name"`
	Results []Agent `json:"results"`
	Total   int     `json:"total"`
}

// ModelConfig represents a single agent model configuration returned by the backend.
type ModelConfig struct {
	CUID string `json:"cuid"`
	Name string `json:"name"`
}

// ModelConfigsResponse is the response envelope for the model-configs endpoint.
type ModelConfigsResponse struct {
	Items []ModelConfig `json:"items"`
	Total int           `json:"total"`
}

type Client struct {
	baseURL       string
	machineKey    string
	machineSecret string
	httpClient    *http.Client
}

func NewClient(cfg config.BackendConfig) (*Client, error) {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		return nil, nil
	}
	if strings.TrimSpace(cfg.MachineKey) == "" || strings.TrimSpace(cfg.MachineSecret) == "" {
		return nil, fmt.Errorf("backend machine credentials are required when backend.base_url is configured")
	}
	if _, err := url.ParseRequestURI(baseURL); err != nil {
		return nil, fmt.Errorf("backend base_url is invalid: %w", err)
	}

	return &Client{
		baseURL:       strings.TrimRight(baseURL, "/"),
		machineKey:    strings.TrimSpace(cfg.MachineKey),
		machineSecret: strings.TrimSpace(cfg.MachineSecret),
		httpClient:    &http.Client{Timeout: time.Duration(cfg.ConnectionTimeoutSecs) * time.Second},
	}, nil
}

// FetchAgents retrieves active inventory model agents from the backend for the
// given org and primary record type slug. It returns an empty slice when the
// backend returns zero results.
func (c *Client) FetchAgents(ctx context.Context, orgCUID, agentRecordTypeSlug string) (AgentsResponse, error) {
	u, err := url.Parse(c.baseURL + agentsPath)
	if err != nil {
		return AgentsResponse{}, fmt.Errorf("build agents URL: %w", err)
	}
	q := u.Query()
	q.Set("org_cuid", orgCUID)
	q.Set("primary_record_type_slug", agentRecordTypeSlug)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return AgentsResponse{}, fmt.Errorf("build agents request: %w", err)
	}
	req.Header.Set("X-MACHINE-KEY", c.machineKey)
	req.Header.Set("X-MACHINE-SECRET", c.machineSecret)
	req.Header.Set("X-Org-CUID", orgCUID)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return AgentsResponse{}, fmt.Errorf("call agents endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return AgentsResponse{}, fmt.Errorf("agents endpoint returned %s", resp.Status)
	}

	var payload AgentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return AgentsResponse{}, fmt.Errorf("decode agents response: %w", err)
	}
	return payload, nil
}

// FetchModelConfigs retrieves all agent model configurations from the backend.
func (c *Client) FetchModelConfigs(ctx context.Context) (ModelConfigsResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+modelConfigsPath, nil)
	if err != nil {
		return ModelConfigsResponse{}, fmt.Errorf("build model-configs request: %w", err)
	}
	req.Header.Set("X-MACHINE-KEY", c.machineKey)
	req.Header.Set("X-MACHINE-SECRET", c.machineSecret)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ModelConfigsResponse{}, fmt.Errorf("call model-configs endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ModelConfigsResponse{}, fmt.Errorf("model-configs endpoint returned %s", resp.Status)
	}

	var payload ModelConfigsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ModelConfigsResponse{}, fmt.Errorf("decode model-configs response: %w", err)
	}
	return payload, nil
}

// EvaluateRequest is sent to the VM backend to ask the LLM whether a tool
// call should be approved or denied.
type EvaluateRequest struct {
	ModelConfigCUID      string         `json:"model_config_cuid"`
	OrgCUID              string         `json:"org_cuid,omitempty"`
	AgentVMCUID          string         `json:"agent_vm_cuid,omitempty"`
	ConstitutionFieldKey string         `json:"constitution_field_key,omitempty"`
	ServerName           string         `json:"server_name"`
	ToolName             string         `json:"tool_name"`
	ToolArgs             map[string]any `json:"tool_args,omitempty"`
	Context              string         `json:"context,omitempty"`
}

// EvaluateResponse is the result returned by the VM backend after LLM evaluation.
type EvaluateResponse struct {
	Approved bool   `json:"approved"`
	Reason   string `json:"reason"`
}

// EvaluateToolCall calls the VM backend's evaluate endpoint and returns whether
// the tool call should be approved.
func (c *Client) EvaluateToolCall(ctx context.Context, req EvaluateRequest) (EvaluateResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return EvaluateResponse{}, fmt.Errorf("marshal evaluate request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.baseURL+evaluatePath,
		strings.NewReader(string(body)),
	)
	if err != nil {
		return EvaluateResponse{}, fmt.Errorf("build evaluate request: %w", err)
	}
	httpReq.Header.Set("X-MACHINE-KEY", c.machineKey)
	httpReq.Header.Set("X-MACHINE-SECRET", c.machineSecret)
	if req.OrgCUID != "" {
		httpReq.Header.Set("X-Org-CUID", req.OrgCUID)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return EvaluateResponse{}, fmt.Errorf("call evaluate endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return EvaluateResponse{}, fmt.Errorf("evaluate endpoint returned %s", resp.Status)
	}

	var payload EvaluateResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return EvaluateResponse{}, fmt.Errorf("decode evaluate response: %w", err)
	}
	return payload, nil
}

func (c *Client) CheckConnection(ctx context.Context) (ConnectionResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+connectionPath, nil)
	if err != nil {
		return ConnectionResponse{}, fmt.Errorf("build backend connection request: %w", err)
	}
	req.Header.Set("X-MACHINE-KEY", c.machineKey)
	req.Header.Set("X-MACHINE-SECRET", c.machineSecret)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ConnectionResponse{}, fmt.Errorf("call backend connection endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ConnectionResponse{}, fmt.Errorf("backend connection endpoint returned %s", resp.Status)
	}

	var payload ConnectionResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ConnectionResponse{}, fmt.Errorf("decode backend connection response: %w", err)
	}
	if !payload.OK {
		return ConnectionResponse{}, fmt.Errorf("backend connection endpoint returned ok=false")
	}
	return payload, nil
}
