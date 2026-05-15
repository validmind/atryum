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

type ConnectionResponse struct {
	OK              bool   `json:"ok"`
	MachineUserCUID string `json:"machine_user_cuid"`
	ServiceName     string `json:"service_name"`
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

	timeout := time.Duration(cfg.ConnectionTimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		baseURL:       strings.TrimRight(baseURL, "/"),
		machineKey:    strings.TrimSpace(cfg.MachineKey),
		machineSecret: strings.TrimSpace(cfg.MachineSecret),
		httpClient:    &http.Client{Timeout: timeout},
	}, nil
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
