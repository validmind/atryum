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
const summarizeInvocationPath = "/internal/v1/atryum/summarize-invocation"
const organizationsPath = "/internal/v1/atryum/organizations"
const primaryRecordTypesPath = "/internal/v1/atryum/primary-record-types"
const customFieldsPath = "/internal/v1/atryum/custom-fields"

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
	// evaluateClient uses a longer timeout suitable for LLM completion calls.
	evaluateClient *http.Client
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
		baseURL:        strings.TrimRight(baseURL, "/"),
		machineKey:     strings.TrimSpace(cfg.MachineKey),
		machineSecret:  strings.TrimSpace(cfg.MachineSecret),
		httpClient:     &http.Client{Timeout: time.Duration(cfg.ConnectionTimeoutSecs) * time.Second},
		evaluateClient: &http.Client{Timeout: time.Duration(cfg.EvaluateTimeoutSecs) * time.Second},
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

// VMOrg represents an organization returned by the backend discovery API.
type VMOrg struct {
	CUID string `json:"cuid"`
	Name string `json:"name"`
}

// VMOrgsResponse is the response envelope for the organizations endpoint.
type VMOrgsResponse struct {
	Items []VMOrg `json:"items"`
	Total int     `json:"total"`
}

// VMRecordType represents a primary record type returned by the backend discovery API.
type VMRecordType struct {
	CUID string `json:"cuid"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// VMRecordTypesResponse is the response envelope for the primary-record-types endpoint.
type VMRecordTypesResponse struct {
	Items []VMRecordType `json:"items"`
	Total int            `json:"total"`
}

// VMCustomField represents a custom field definition returned by the backend discovery API.
type VMCustomField struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	FieldType string `json:"field_type"`
}

// VMCustomFieldsResponse is the response envelope for the custom-fields endpoint.
type VMCustomFieldsResponse struct {
	Items []VMCustomField `json:"items"`
	Total int             `json:"total"`
}

// FetchOrganizations retrieves all organizations from the backend.
func (c *Client) FetchOrganizations(ctx context.Context) (VMOrgsResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+organizationsPath, nil)
	if err != nil {
		return VMOrgsResponse{}, fmt.Errorf("build organizations request: %w", err)
	}
	req.Header.Set("X-MACHINE-KEY", c.machineKey)
	req.Header.Set("X-MACHINE-SECRET", c.machineSecret)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return VMOrgsResponse{}, fmt.Errorf("call organizations endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return VMOrgsResponse{}, fmt.Errorf("organizations endpoint returned %s", resp.Status)
	}

	var payload VMOrgsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return VMOrgsResponse{}, fmt.Errorf("decode organizations response: %w", err)
	}
	return payload, nil
}

// FetchPrimaryRecordTypes retrieves primary record types for the given org.
func (c *Client) FetchPrimaryRecordTypes(ctx context.Context, orgCUID string) (VMRecordTypesResponse, error) {
	u, err := url.Parse(c.baseURL + primaryRecordTypesPath)
	if err != nil {
		return VMRecordTypesResponse{}, fmt.Errorf("build primary-record-types URL: %w", err)
	}
	q := u.Query()
	q.Set("org_cuid", orgCUID)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return VMRecordTypesResponse{}, fmt.Errorf("build primary-record-types request: %w", err)
	}
	req.Header.Set("X-MACHINE-KEY", c.machineKey)
	req.Header.Set("X-MACHINE-SECRET", c.machineSecret)
	req.Header.Set("X-Org-CUID", orgCUID)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return VMRecordTypesResponse{}, fmt.Errorf("call primary-record-types endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return VMRecordTypesResponse{}, fmt.Errorf("primary-record-types endpoint returned %s", resp.Status)
	}

	var payload VMRecordTypesResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return VMRecordTypesResponse{}, fmt.Errorf("decode primary-record-types response: %w", err)
	}
	return payload, nil
}

// FetchCustomFields retrieves custom field definitions from the backend for the
// given org, optionally filtered to a specific primary record type slug.
func (c *Client) FetchCustomFields(ctx context.Context, orgCUID, primaryRecordTypeSlug string) (VMCustomFieldsResponse, error) {
	u, err := url.Parse(c.baseURL + customFieldsPath)
	if err != nil {
		return VMCustomFieldsResponse{}, fmt.Errorf("build custom-fields URL: %w", err)
	}
	q := u.Query()
	q.Set("org_cuid", orgCUID)
	if primaryRecordTypeSlug != "" {
		q.Set("primary_record_type_slug", primaryRecordTypeSlug)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return VMCustomFieldsResponse{}, fmt.Errorf("build custom-fields request: %w", err)
	}
	req.Header.Set("X-MACHINE-KEY", c.machineKey)
	req.Header.Set("X-MACHINE-SECRET", c.machineSecret)
	req.Header.Set("X-Org-CUID", orgCUID)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return VMCustomFieldsResponse{}, fmt.Errorf("call custom-fields endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return VMCustomFieldsResponse{}, fmt.Errorf("custom-fields endpoint returned %s", resp.Status)
	}

	var payload VMCustomFieldsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return VMCustomFieldsResponse{}, fmt.Errorf("decode custom-fields response: %w", err)
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
// Verdict is one of: "approved", "denied", "human_approval", "next_rule".
type EvaluateResponse struct {
	Verdict    string   `json:"verdict"`
	Reason     string   `json:"reason"`
	Confidence *float64 `json:"confidence,omitempty"`
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

	resp, err := c.evaluateClient.Do(httpReq)
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

// SummarizeInvocationRequest is sent to the VM backend to ask an LLM to
// produce a short human-readable summary of a single invocation.
type SummarizeInvocationRequest struct {
	ModelConfigCUID string         `json:"model_config_cuid"`
	OrgCUID         string         `json:"org_cuid,omitempty"`
	Invocation      map[string]any `json:"invocation"`
}

// SummarizeInvocationResponse is the result returned by the VM backend after
// LLM summarization.
type SummarizeInvocationResponse struct {
	Summary string `json:"summary"`
}

// SummarizeInvocation calls the VM backend's summarize-invocation endpoint and
// returns the produced summary.
func (c *Client) SummarizeInvocation(ctx context.Context, req SummarizeInvocationRequest) (SummarizeInvocationResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return SummarizeInvocationResponse{}, fmt.Errorf("marshal summarize-invocation request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.baseURL+summarizeInvocationPath,
		strings.NewReader(string(body)),
	)
	if err != nil {
		return SummarizeInvocationResponse{}, fmt.Errorf("build summarize-invocation request: %w", err)
	}
	httpReq.Header.Set("X-MACHINE-KEY", c.machineKey)
	httpReq.Header.Set("X-MACHINE-SECRET", c.machineSecret)
	if req.OrgCUID != "" {
		httpReq.Header.Set("X-Org-CUID", req.OrgCUID)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	// Reuse the longer-timeout client since this is an LLM completion call.
	resp, err := c.evaluateClient.Do(httpReq)
	if err != nil {
		return SummarizeInvocationResponse{}, fmt.Errorf("call summarize-invocation endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SummarizeInvocationResponse{}, fmt.Errorf("summarize-invocation endpoint returned %s", resp.Status)
	}

	var payload SummarizeInvocationResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return SummarizeInvocationResponse{}, fmt.Errorf("decode summarize-invocation response: %w", err)
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
