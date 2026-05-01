package mcp

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"atryum/internal/config"
)

type UpstreamMode string

const (
	UpstreamModeHTTP  UpstreamMode = "http"
	UpstreamModeStdio UpstreamMode = "stdio"
)

type ServerConnectionStatus string
type ServerAuthStatus string
type ServerAuthType string

const (
	ConnectionStatusUnknown        ServerConnectionStatus = "unknown"
	ConnectionStatusReady          ServerConnectionStatus = "ready"
	ConnectionStatusUnreachable    ServerConnectionStatus = "unreachable"
	ConnectionStatusDegraded       ServerConnectionStatus = "degraded"
	ConnectionStatusDisabled       ServerConnectionStatus = "disabled"
	ConnectionStatusNeedsAttention ServerConnectionStatus = "needs_attention"

	AuthStatusUnknown            ServerAuthStatus = "unknown"
	AuthStatusReady              ServerAuthStatus = "ready"
	AuthStatusMissingCredentials ServerAuthStatus = "missing_credentials"
	AuthStatusInvalid            ServerAuthStatus = "invalid"
	AuthStatusReauthNeeded       ServerAuthStatus = "reauth_needed"
	AuthStatusNotApplicable      ServerAuthStatus = "not_applicable"

	AuthTypeNone   ServerAuthType = "none"
	AuthTypeBearer ServerAuthType = "bearer_token"
	AuthTypeEnv    ServerAuthType = "env_token"
	AuthTypeHosted ServerAuthType = "hosted_or_oauth"
)

type ServerStatus struct {
	AuthType         ServerAuthType         `json:"auth_type"`
	ConnectionStatus ServerConnectionStatus `json:"connection_status"`
	AuthStatus       ServerAuthStatus       `json:"auth_status"`
	ReauthNeeded     bool                   `json:"reauth_needed"`
	LastCheckedAt    *time.Time             `json:"last_checked_at,omitempty"`
	LastCheckOK      bool                   `json:"last_check_ok"`
	LastErrorSummary *string                `json:"last_error_summary,omitempty"`
	ActionRequired   *string                `json:"action_required,omitempty"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type Upstream struct {
	Name      string
	Mode      UpstreamMode
	BaseURL   string
	AuthToken string
	Timeout   time.Duration
	Command   string
	Args      []string
	Env       map[string]string
	Enabled   bool
	Status    ServerStatus
}

type ServerStore interface {
	GetServer(ctx context.Context, name string) (Upstream, error)
	ListServers(ctx context.Context, filter ServerFilter) ([]Upstream, int, error)
	CountServers(ctx context.Context) (int, error)
	CreateServer(ctx context.Context, upstream Upstream) error
}

type ServerFilter struct {
	Offset  uint64
	Limit   uint64
	Enabled *bool
}

type Resolver struct {
	store     ServerStore
	bootstrap map[string]Upstream
}

type Client struct {
	httpClient *http.Client
	nextID     atomic.Int64
	debug      bool
}

type InvokeResult struct {
	StatusCode int
	Body       []byte
	Failed     bool
}

type ConnectionTestResult struct {
	Ok               bool
	Message          string
	ConnectionStatus ServerConnectionStatus
	AuthStatus       ServerAuthStatus
	ReauthNeeded     bool
	LastCheckOK      bool
	LastErrorSummary *string
	ActionRequired   *string
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func NewResolver(store ServerStore, cfg config.Config) *Resolver {
	bootstrap := make(map[string]Upstream)
	for _, u := range cfg.Upstreams {
		upstream := fromConfig(u)
		if !upstream.Enabled {
			continue
		}
		bootstrap[upstream.Name] = upstream
	}
	return &Resolver{store: store, bootstrap: bootstrap}
}

func NewHTTPClient() *Client {
	debug := strings.EqualFold(os.Getenv("ATRYUM_MCP_DEBUG"), "1") || strings.EqualFold(os.Getenv("ATRYUM_MCP_DEBUG"), "true")
	return &Client{httpClient: &http.Client{}, debug: debug}
}

func (r *Resolver) Resolve(name string) (Upstream, error) {
	return r.ResolveContext(context.Background(), name)
}

func (r *Resolver) ResolveContext(ctx context.Context, name string) (Upstream, error) {
	if r.store != nil {
		upstream, err := r.store.GetServer(ctx, name)
		if err == nil {
			return upstream, nil
		}
		if err != nil && err != sql.ErrNoRows {
			return Upstream{}, err
		}
	}
	upstream, ok := r.bootstrap[name]
	if !ok || !upstream.Enabled {
		return Upstream{}, fmt.Errorf("upstream %q not configured or disabled", name)
	}
	return upstream, nil
}

func (r *Resolver) BootstrapIfEmpty(ctx context.Context) error {
	if r.store == nil {
		return nil
	}
	count, err := r.store.CountServers(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	for _, upstream := range r.bootstrap {
		if err := r.store.CreateServer(ctx, upstream); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) Invoke(ctx context.Context, upstream Upstream, tool string, input map[string]any, requestID *string) (InvokeResult, error) {
	started := time.Now()
	defer func() {
		c.debugf("upstream invoke transport=%s server=%s tool=%s duration_ms=%d", upstream.Mode, upstream.Name, tool, time.Since(started).Milliseconds())
	}()
	switch upstream.Mode {
	case UpstreamModeStdio:
		return c.invokeStdio(ctx, upstream, tool, input)
	case UpstreamModeHTTP, "":
		return c.invokeHTTP(ctx, upstream, tool, input, requestID)
	default:
		return InvokeResult{}, fmt.Errorf("unsupported upstream mode %q", upstream.Mode)
	}
}

func (c *Client) ListTools(ctx context.Context, upstream Upstream) ([]Tool, error) {
	started := time.Now()
	defer func() {
		c.debugf("upstream tools.list transport=%s server=%s duration_ms=%d", upstream.Mode, upstream.Name, time.Since(started).Milliseconds())
	}()
	switch upstream.Mode {
	case UpstreamModeStdio:
		return c.listToolsStdio(ctx, upstream)
	case UpstreamModeHTTP, "":
		return c.listToolsHTTP(ctx, upstream)
	default:
		return nil, fmt.Errorf("unsupported upstream mode %q", upstream.Mode)
	}
}

func (c *Client) TestConnection(ctx context.Context, upstream Upstream) ConnectionTestResult {
	if !upstream.Enabled {
		message := "server is disabled"
		action := "enable the server before using it"
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusDisabled, AuthStatus: upstream.Status.AuthStatus, ReauthNeeded: false, LastCheckOK: false, LastErrorSummary: stringPtr(message), ActionRequired: &action}
	}
	switch upstream.Mode {
	case UpstreamModeHTTP:
		return c.testHTTP(ctx, upstream)
	case UpstreamModeStdio:
		return c.testStdio(ctx, upstream)
	default:
		message := fmt.Sprintf("unsupported mode %q", upstream.Mode)
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusNeedsAttention, AuthStatus: AuthStatusUnknown, ReauthNeeded: false, LastCheckOK: false, LastErrorSummary: stringPtr(message)}
	}
}

func (c *Client) invokeHTTP(ctx context.Context, upstream Upstream, tool string, input map[string]any, requestID *string) (InvokeResult, error) {
	payload := map[string]any{"tool": tool, "input": input}
	if requestID != nil {
		payload["request_id"] = *requestID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return InvokeResult{}, err
	}
	endpoint := upstream.BaseURL + "/mcp/tools/call"
	client := c.httpClient
	if upstream.Timeout > 0 {
		client = &http.Client{Timeout: upstream.Timeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return InvokeResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if upstream.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+upstream.AuthToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return InvokeResult{}, err
	}
	defer resp.Body.Close()
	respBody := new(bytes.Buffer)
	_, err = respBody.ReadFrom(resp.Body)
	if err != nil {
		return InvokeResult{}, err
	}
	bodyBytes := respBody.Bytes()
	failed := resp.StatusCode >= http.StatusBadRequest || looksLikeToolError(bodyBytes)
	c.debugf("upstream http tools.call server=%s status=%d failed=%t", upstream.Name, resp.StatusCode, failed)
	return InvokeResult{StatusCode: resp.StatusCode, Body: bodyBytes, Failed: failed}, nil
}

func (c *Client) listToolsHTTP(ctx context.Context, upstream Upstream) ([]Tool, error) {
	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(upstream.BaseURL, "/")
	client := c.httpClient
	if upstream.Timeout > 0 {
		client = &http.Client{Timeout: upstream.Timeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if upstream.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+upstream.AuthToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody := new(bytes.Buffer)
	_, err = respBody.ReadFrom(resp.Body)
	if err != nil {
		return nil, err
	}
	var rpcResp rpcResponse
	if err := json.Unmarshal(respBody.Bytes(), &rpcResp); err != nil {
		return nil, err
	}
	if len(rpcResp.Error) > 0 && string(rpcResp.Error) != "null" {
		return nil, fmt.Errorf("upstream tools/list error: %s", string(rpcResp.Error))
	}
	return decodeToolsListResult(rpcResp.Result)
}

func (c *Client) invokeStdio(ctx context.Context, upstream Upstream, tool string, input map[string]any) (InvokeResult, error) {
	if upstream.Command == "" {
		return InvokeResult{}, fmt.Errorf("stdio upstream %q missing command", upstream.Name)
	}
	cmd := exec.CommandContext(ctx, upstream.Command, upstream.Args...)
	cmd.Env = os.Environ()
	for k, v := range upstream.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return InvokeResult{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return InvokeResult{}, err
	}
	stderr := new(bytes.Buffer)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return InvokeResult{}, err
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	reader := bufio.NewReader(stdout)
	if err := writeRPC(stdin, c.nextRPCID(), "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]any{"name": "atryum", "version": "0.1.0"},
		"capabilities":    map[string]any{},
	}); err != nil {
		return InvokeResult{}, err
	}
	if _, err := readRPC(reader); err != nil {
		return InvokeResult{}, err
	}
	_ = writeRPC(stdin, c.nextRPCID(), "notifications/initialized", map[string]any{})
	if err := writeRPC(stdin, c.nextRPCID(), "tools/call", map[string]any{
		"name":      tool,
		"arguments": input,
	}); err != nil {
		return InvokeResult{}, err
	}
	resp, err := readRPC(reader)
	if err != nil {
		if stderr.Len() > 0 {
			return InvokeResult{}, fmt.Errorf("stdio upstream error: %s", strings.TrimSpace(stderr.String()))
		}
		return InvokeResult{}, err
	}
	if len(resp.Error) > 0 && string(resp.Error) != "null" {
		return InvokeResult{StatusCode: http.StatusBadGateway, Body: resp.Error, Failed: true}, nil
	}
	body := resp.Result
	if len(body) == 0 {
		body = []byte(`{"ok":true}`)
	}
	return InvokeResult{StatusCode: http.StatusOK, Body: body, Failed: looksLikeToolError(body)}, nil
}

func (c *Client) listToolsStdio(ctx context.Context, upstream Upstream) ([]Tool, error) {
	if upstream.Command == "" {
		return nil, fmt.Errorf("stdio upstream %q missing command", upstream.Name)
	}
	cmd := exec.CommandContext(ctx, upstream.Command, upstream.Args...)
	cmd.Env = os.Environ()
	for k, v := range upstream.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := new(bytes.Buffer)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()
	reader := bufio.NewReader(stdout)
	if err := writeRPC(stdin, c.nextRPCID(), "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]any{"name": "atryum", "version": "0.1.0"},
		"capabilities":    map[string]any{},
	}); err != nil {
		return nil, err
	}
	if _, err := readRPC(reader); err != nil {
		return nil, err
	}
	_ = writeRPC(stdin, c.nextRPCID(), "notifications/initialized", map[string]any{})
	if err := writeRPC(stdin, c.nextRPCID(), "tools/list", map[string]any{}); err != nil {
		return nil, err
	}
	resp, err := readRPC(reader)
	if err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("stdio upstream error: %s", strings.TrimSpace(stderr.String()))
		}
		return nil, err
	}
	if len(resp.Error) > 0 && string(resp.Error) != "null" {
		return nil, fmt.Errorf("tools/list error: %s", string(resp.Error))
	}
	return decodeToolsListResult(resp.Result)
}

func FromConfig(cfg config.UpstreamConfig) Upstream {
	return fromConfig(cfg)
}

func fromConfig(u config.UpstreamConfig) Upstream {
	mode := UpstreamModeHTTP
	if u.Mode != "" {
		mode = UpstreamMode(u.Mode)
	}
	upstream := Upstream{
		Name:      u.Name,
		Mode:      mode,
		BaseURL:   strings.TrimRight(u.BaseURL, "/"),
		AuthToken: u.AuthToken,
		Timeout:   time.Duration(u.TimeoutSeconds) * time.Second,
		Command:   u.Command,
		Args:      append([]string(nil), u.Args...),
		Env:       cloneMap(u.Env),
		Enabled:   u.Enabled,
	}
	upstream.Status = inferInitialStatus(upstream)
	return upstream
}

func inferInitialStatus(upstream Upstream) ServerStatus {
	authType := inferAuthType(upstream)
	authStatus := inferAuthStatus(upstream, authType)
	connectionStatus := ConnectionStatusUnknown
	if !upstream.Enabled {
		connectionStatus = ConnectionStatusDisabled
	}
	actionRequired := inferActionRequired(upstream, authStatus)
	return ServerStatus{
		AuthType:         authType,
		ConnectionStatus: connectionStatus,
		AuthStatus:       authStatus,
		ReauthNeeded:     authStatus == AuthStatusReauthNeeded,
		LastCheckOK:      false,
		ActionRequired:   actionRequired,
	}
}

func inferAuthType(upstream Upstream) ServerAuthType {
	if upstream.Mode == UpstreamModeHTTP {
		if strings.TrimSpace(upstream.AuthToken) != "" {
			return AuthTypeBearer
		}
		return AuthTypeHosted
	}
	if hasCredentialEnv(upstream.Env) {
		return AuthTypeEnv
	}
	return AuthTypeNone
}

func inferAuthStatus(upstream Upstream, authType ServerAuthType) ServerAuthStatus {
	switch upstream.Mode {
	case UpstreamModeHTTP:
		if strings.TrimSpace(upstream.BaseURL) == "" {
			return AuthStatusUnknown
		}
		if authType == AuthTypeBearer && strings.TrimSpace(upstream.AuthToken) == "" {
			return AuthStatusMissingCredentials
		}
		return AuthStatusUnknown
	case UpstreamModeStdio:
		if strings.TrimSpace(upstream.Command) == "" {
			return AuthStatusUnknown
		}
		if authType == AuthTypeEnv && !hasCredentialEnv(upstream.Env) {
			return AuthStatusMissingCredentials
		}
		return AuthStatusUnknown
	default:
		return AuthStatusUnknown
	}
}

func inferActionRequired(upstream Upstream, authStatus ServerAuthStatus) *string {
	if !upstream.Enabled {
		message := "enable the server to use it"
		return &message
	}
	if authStatus == AuthStatusMissingCredentials {
		if upstream.Mode == UpstreamModeHTTP {
			message := "add credentials or verify the hosted server auth flow"
			return &message
		}
		message := "add required credential environment variables"
		return &message
	}
	return nil
}

func (c *Client) testHTTP(ctx context.Context, upstream Upstream) ConnectionTestResult {
	if strings.TrimSpace(upstream.BaseURL) == "" {
		message := "missing base_url"
		action := "set base_url for this server"
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusNeedsAttention, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message, ActionRequired: &action}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, upstream.BaseURL, nil)
	if err != nil {
		message := err.Error()
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusNeedsAttention, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		message := err.Error()
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusUnreachable, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		message := fmt.Sprintf("http %d", resp.StatusCode)
		action := "refresh or update credentials"
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusNeedsAttention, AuthStatus: AuthStatusInvalid, ReauthNeeded: true, LastCheckOK: false, LastErrorSummary: &message, ActionRequired: &action}
	}
	if resp.StatusCode >= http.StatusBadRequest {
		message := fmt.Sprintf("http %d", resp.StatusCode)
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusDegraded, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message}
	}
	message := fmt.Sprintf("http %d", resp.StatusCode)
	return ConnectionTestResult{Ok: true, Message: message, ConnectionStatus: ConnectionStatusReady, AuthStatus: AuthStatusReady, ReauthNeeded: false, LastCheckOK: true}
}

func (c *Client) testStdio(ctx context.Context, upstream Upstream) ConnectionTestResult {
	if strings.TrimSpace(upstream.Command) == "" {
		message := "missing command"
		action := "set a command for this stdio server"
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusNeedsAttention, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message, ActionRequired: &action}
	}
	if strings.Contains(strings.ToLower(upstream.Name), "shortcut") && !hasCredentialEnv(upstream.Env) {
		message := "missing credential environment variable"
		action := "add the required API token env var"
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusNeedsAttention, AuthStatus: AuthStatusMissingCredentials, LastCheckOK: false, LastErrorSummary: &message, ActionRequired: &action}
	}
	cmd := exec.CommandContext(ctx, upstream.Command, upstream.Args...)
	cmd.Env = os.Environ()
	for k, v := range upstream.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		message := err.Error()
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusUnreachable, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		message := err.Error()
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusUnreachable, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message}
	}
	stderr := new(bytes.Buffer)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		message := err.Error()
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusUnreachable, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message}
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()
	reader := bufio.NewReader(stdout)
	if err := writeRPC(stdin, c.nextRPCID(), "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]any{"name": "atryum", "version": "0.1.0"},
		"capabilities":    map[string]any{},
	}); err != nil {
		message := err.Error()
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusUnreachable, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message}
	}
	if _, err := readRPC(reader); err != nil {
		message := err.Error()
		if stderr.Len() > 0 {
			message = strings.TrimSpace(stderr.String())
		}
		action := classifyActionRequired(message)
		authStatus, reauth := classifyAuthFailure(message)
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusNeedsAttention, AuthStatus: authStatus, ReauthNeeded: reauth, LastCheckOK: false, LastErrorSummary: &message, ActionRequired: action}
	}
	return ConnectionTestResult{Ok: true, Message: "stdio initialize ok", ConnectionStatus: ConnectionStatusReady, AuthStatus: AuthStatusReady, ReauthNeeded: false, LastCheckOK: true}
}

func classifyAuthFailure(message string) (ServerAuthStatus, bool) {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "unauthorized"), strings.Contains(lower, "forbidden"), strings.Contains(lower, "invalid token"):
		return AuthStatusInvalid, true
	case strings.Contains(lower, "login"), strings.Contains(lower, "reauth"), strings.Contains(lower, "expired"):
		return AuthStatusReauthNeeded, true
	case strings.Contains(lower, "token"), strings.Contains(lower, "credential"), strings.Contains(lower, "api key"):
		return AuthStatusMissingCredentials, false
	default:
		return AuthStatusUnknown, false
	}
}

func classifyActionRequired(message string) *string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "unauthorized"), strings.Contains(lower, "forbidden"), strings.Contains(lower, "invalid token"):
		action := "refresh or replace credentials"
		return &action
	case strings.Contains(lower, "expired"), strings.Contains(lower, "reauth"), strings.Contains(lower, "login"):
		action := "reauthenticate with the upstream service"
		return &action
	case strings.Contains(lower, "token"), strings.Contains(lower, "credential"), strings.Contains(lower, "api key"):
		action := "add required credentials"
		return &action
	default:
		return nil
	}
}

func hasCredentialEnv(env map[string]string) bool {
	for key, value := range env {
		upper := strings.ToUpper(key)
		if value == "" {
			continue
		}
		if strings.Contains(upper, "TOKEN") || strings.Contains(upper, "KEY") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") {
			return true
		}
	}
	return false
}

func stringPtr(v string) *string { return &v }

func writeRPC(w interface{ Write([]byte) (int, error) }, id int64, method string, params map[string]any) error {
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if strings.HasPrefix(method, "notifications/") {
		delete(msg, "id")
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func readRPC(reader *bufio.Reader) (rpcResponse, error) {
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return rpcResponse{}, err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		if len(resp.ID) == 0 && len(resp.Result) == 0 && len(resp.Error) == 0 {
			continue
		}
		return resp, nil
	}
}

func looksLikeToolError(body []byte) bool {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	if isErr, ok := payload["isError"].(bool); ok && isErr {
		return true
	}
	if content, ok := payload["content"].([]any); ok {
		for _, item := range content {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if isErr, ok := obj["isError"].(bool); ok && isErr {
				return true
			}
			if text, ok := obj["text"].(string); ok && strings.Contains(strings.ToLower(text), "not found") {
				return true
			}
		}
	}
	return false
}

func decodeToolsListResult(result json.RawMessage) ([]Tool, error) {
	var payload struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(result, &payload); err == nil && payload.Tools != nil {
		return payload.Tools, nil
	}
	var bare []Tool
	if err := json.Unmarshal(result, &bare); err == nil {
		return bare, nil
	}
	return nil, fmt.Errorf("unsupported tools/list result shape")
}

func (c *Client) nextRPCID() int64 {
	return c.nextID.Add(1)
}

func cloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (c *Client) debugf(format string, args ...any) {
	if !c.debug {
		return
	}
	log.Printf("[mcp] "+format, args...)
}
