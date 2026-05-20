package mcp

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"atryum/internal/config"
)

type UpstreamMode string

const (
	UpstreamModeHTTP  UpstreamMode = "http"
	UpstreamModeStdio UpstreamMode = "stdio"
)

const (
	MCPProtocolVersion2024    = "2024-11-05"
	MCPProtocolVersion2025    = "2025-11-25"
	DefaultMCPProtocolVersion = MCPProtocolVersion2025
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

type OAuthToken struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	Scope        string
	ExpiresAt    *time.Time
}

type AuthHeader struct {
	Name  string
	Value string
}

type Upstream struct {
	Name               string
	Mode               UpstreamMode
	BaseURL            string
	AuthToken          string
	AuthHeaders        []AuthHeader
	Timeout            time.Duration
	Command            string
	Args               []string
	Env                map[string]string
	Enabled            bool
	Status             ServerStatus
	OAuthProviderID    string
	OAuthProviderLabel string
	OAuthAuthorizeURL  string
	OAuthTokenURL      string
	OAuthClientID      string
	OAuthClientSecret  string
	OAuthScopes        string
	// OAuthClientRegistration records HOW the OAuth client_id was obtained,
	// independent of which strategy is now driving the live flow. After DCR
	// runs, OAuthProviderID flips to oauth_pkce / oauth_client_secret
	// because that's operationally what's happening — this field preserves
	// the registration provenance so the UI can label it correctly and we
	// can act on the distinction later (e.g. "re-register" buttons).
	OAuthClientRegistration ClientRegistration
}

type ClientRegistration string

const (
	ClientRegistrationUnknown   ClientRegistration = ""
	ClientRegistrationPreshared ClientRegistration = "preshared"
	ClientRegistrationDynamic   ClientRegistration = "dynamic"
	ClientRegistrationCIMD      ClientRegistration = "cimd"
)

type Envelope struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func (e Envelope) IsNotification() bool {
	return strings.TrimSpace(e.Method) != "" && len(e.ID) == 0
}

func (e Envelope) IsRequest() bool {
	return strings.TrimSpace(e.Method) != "" && len(e.ID) > 0
}

func EncodeAuthHeaders(headers []AuthHeader) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for _, header := range headers {
		name := strings.TrimSpace(header.Name)
		value := strings.TrimSpace(header.Value)
		if name == "" || value == "" {
			continue
		}
		out[name] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func DecodeAuthHeaders(env map[string]string) []AuthHeader {
	if len(env) == 0 {
		return nil
	}
	out := make([]AuthHeader, 0, len(env))
	for name, value := range env {
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" || value == "" {
			continue
		}
		out = append(out, AuthHeader{Name: name, Value: value})
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

// CredentialStore returns the OAuth access token currently stored for a
// server. It is consulted on every ResolveContext so that proxied MCP
// requests (tools/list, tools/call, forward) get the live access token
// attached. The interface is intentionally narrow — the resolver does not
// need to know about refresh tokens or expiry; that's the credential
// store's problem.
type CredentialStore interface {
	GetCredential(ctx context.Context, serverName string) (AccessTokenView, error)
}

type AccessTokenView struct {
	AccessToken string
}

type Resolver struct {
	store       ServerStore
	credentials CredentialStore
	bootstrap   map[string]Upstream
}

type Client struct {
	httpClient *http.Client
	nextID     atomic.Int64
	debug      bool

	// MCP Streamable HTTP session IDs per upstream. The transport
	// (since spec rev 2025-03-26) lets a server require an `Mcp-Session-Id`
	// header on every non-initialize request. We initialize lazily, cache
	// the id, and clear it on 404 (server-side expiry) so the next call
	// re-inits. Stateless upstreams (those that never return the header)
	// stay as empty strings.
	sessionMu sync.Mutex
	sessions  map[string]string
}

type InvokeResult struct {
	StatusCode int
	Body       []byte
	Failed     bool
}

type ForwardResult struct {
	StatusCode      int
	Body            []byte
	ContentType     string
	ProtocolVersion string
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

// WithCredentials returns the resolver wired up with an OAuth credential
// store. When set, ResolveContext overlays the stored access token onto
// upstream.AuthToken so that downstream HTTP requests carry it as a
// Bearer header.
func (r *Resolver) WithCredentials(credentials CredentialStore) *Resolver {
	r.credentials = credentials
	return r
}

func NewHTTPClient() *Client {
	debug := strings.EqualFold(os.Getenv("ATRYUM_MCP_DEBUG"), "1") || strings.EqualFold(os.Getenv("ATRYUM_MCP_DEBUG"), "true")
	return &Client{httpClient: &http.Client{}, debug: debug, sessions: make(map[string]string)}
}

func (r *Resolver) Resolve(name string) (Upstream, error) {
	return r.ResolveContext(context.Background(), name)
}

func (r *Resolver) ResolveContext(ctx context.Context, name string) (Upstream, error) {
	if r.store != nil {
		upstream, err := r.store.GetServer(ctx, name)
		if err == nil {
			return r.overlayCredential(ctx, upstream), nil
		}
		if err != nil && err != sql.ErrNoRows {
			return Upstream{}, err
		}
	}
	upstream, ok := r.bootstrap[name]
	if !ok || !upstream.Enabled {
		return Upstream{}, fmt.Errorf("upstream %q not configured or disabled", name)
	}
	return r.overlayCredential(ctx, upstream), nil
}

// overlayCredential injects the live OAuth access token (when one is
// stored) onto upstream.AuthToken so that applyAuthHeaders adds the
// Bearer header to forwarded MCP requests. The DB never persists the
// access token onto mcp_servers.auth_token — it lives in
// oauth_credentials and is replayed on each resolve.
func (r *Resolver) overlayCredential(ctx context.Context, upstream Upstream) Upstream {
	if r.credentials == nil {
		return upstream
	}
	cred, err := r.credentials.GetCredential(ctx, upstream.Name)
	if err != nil || strings.TrimSpace(cred.AccessToken) == "" {
		return upstream
	}
	upstream.AuthToken = cred.AccessToken
	return upstream
}

func (r *Resolver) ListAll(ctx context.Context) ([]Upstream, error) {
	if r.store != nil {
		enabled := true
		upstreams, _, err := r.store.ListServers(ctx, ServerFilter{Enabled: &enabled})
		if err != nil {
			return nil, err
		}
		if len(upstreams) > 0 {
			for i := range upstreams {
				upstreams[i] = r.overlayCredential(ctx, upstreams[i])
			}
			return upstreams, nil
		}
	}
	var result []Upstream
	for _, u := range r.bootstrap {
		if u.Enabled {
			result = append(result, r.overlayCredential(ctx, u))
		}
	}
	return result, nil
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

func (c *Client) ForwardEnvelope(ctx context.Context, upstream Upstream, envelope Envelope, protocolVersion string) (ForwardResult, error) {
	started := time.Now()
	defer func() {
		c.debugf("upstream forward transport=%s server=%s method=%s duration_ms=%d", upstream.Mode, upstream.Name, envelope.Method, time.Since(started).Milliseconds())
	}()
	switch upstream.Mode {
	case UpstreamModeHTTP, "":
		return c.forwardEnvelopeHTTP(ctx, upstream, envelope, protocolVersion)
	case UpstreamModeStdio:
		return ForwardResult{}, fmt.Errorf("generic stdio forwarding is not implemented")
	default:
		return ForwardResult{}, fmt.Errorf("unsupported upstream mode %q", upstream.Mode)
	}
}

func (c *Client) ExchangeOAuthCode(ctx context.Context, upstream Upstream, code string, redirectURI string, codeVerifier string) (OAuthToken, error) {
	if strings.TrimSpace(upstream.OAuthTokenURL) == "" {
		return OAuthToken{}, fmt.Errorf("oauth_token_url is required")
	}
	if strings.TrimSpace(upstream.OAuthClientID) == "" {
		return OAuthToken{}, fmt.Errorf("oauth client id is required")
	}
	if strings.TrimSpace(upstream.OAuthClientSecret) == "" && strings.TrimSpace(codeVerifier) == "" {
		return OAuthToken{}, fmt.Errorf("oauth client secret or pkce code verifier is required")
	}
	values := url.Values{}
	values.Set("grant_type", "authorization_code")
	values.Set("code", code)
	values.Set("redirect_uri", redirectURI)
	values.Set("client_id", upstream.OAuthClientID)
	if strings.TrimSpace(upstream.OAuthClientSecret) != "" {
		values.Set("client_secret", upstream.OAuthClientSecret)
	}
	if strings.TrimSpace(codeVerifier) != "" {
		values.Set("code_verifier", codeVerifier)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.OAuthTokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return OAuthToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	c.debugf("oauth token exchange server=%s token_url=%s client_id=%s has_secret=%t has_verifier=%t redirect_uri=%s", upstream.Name, upstream.OAuthTokenURL, upstream.OAuthClientID, strings.TrimSpace(upstream.OAuthClientSecret) != "", strings.TrimSpace(codeVerifier) != "", redirectURI)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.debugf("oauth token exchange transport error server=%s err=%v", upstream.Name, err)
		return OAuthToken{}, err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	c.debugf("oauth token exchange response server=%s status=%d body=%s", upstream.Name, resp.StatusCode, truncateForLog(bodyBytes, 600))
	var payload struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		TokenType        string `json:"token_type"`
		Scope            string `json:"scope"`
		ExpiresIn        int    `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.Unmarshal(bodyBytes, &payload)
	if resp.StatusCode >= http.StatusBadRequest || payload.Error != "" || payload.AccessToken == "" {
		// Surface as much of the AS response as we can — the bare status is
		// useless for diagnosing why an OAuth exchange failed.
		snippet := strings.TrimSpace(string(bodyBytes))
		if len(snippet) > 400 {
			snippet = snippet[:400] + "…"
		}
		switch {
		case payload.Error != "" && payload.ErrorDescription != "":
			return OAuthToken{}, fmt.Errorf("oauth token exchange failed (status %d): %s — %s", resp.StatusCode, payload.Error, payload.ErrorDescription)
		case payload.Error != "":
			return OAuthToken{}, fmt.Errorf("oauth token exchange failed (status %d): %s", resp.StatusCode, payload.Error)
		case snippet != "":
			return OAuthToken{}, fmt.Errorf("oauth token exchange failed (status %d): %s", resp.StatusCode, snippet)
		default:
			return OAuthToken{}, fmt.Errorf("oauth token exchange failed with status %d (empty body)", resp.StatusCode)
		}
	}
	var expiresAt *time.Time
	if payload.ExpiresIn > 0 {
		t := time.Now().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second)
		expiresAt = &t
	}
	return OAuthToken{AccessToken: payload.AccessToken, RefreshToken: payload.RefreshToken, TokenType: payload.TokenType, Scope: payload.Scope, ExpiresAt: expiresAt}, nil
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
	if err := c.ensureHTTPSession(ctx, upstream); err != nil {
		return InvokeResult{}, err
	}
	params := map[string]any{"name": tool, "arguments": input}
	if requestID != nil && *requestID != "" {
		params["_meta"] = map[string]any{"atryumRequestId": *requestID}
	}
	body, err := json.Marshal(Envelope{JSONRPC: "2.0", ID: json.RawMessage([]byte("1")), Method: "tools/call", Params: mustRawJSON(params)})
	if err != nil {
		return InvokeResult{}, err
	}
	result, err := c.doHTTPEnvelope(ctx, upstream, body, DefaultMCPProtocolVersion)
	if err != nil {
		return InvokeResult{}, err
	}
	var rpcResp rpcResponse
	if err := json.Unmarshal(result.Body, &rpcResp); err != nil {
		return InvokeResult{}, err
	}
	if len(rpcResp.Error) > 0 && string(rpcResp.Error) != "null" {
		return InvokeResult{StatusCode: result.StatusCode, Body: rpcResp.Error, Failed: true}, nil
	}
	bodyBytes := rpcResp.Result
	if len(bodyBytes) == 0 || string(bodyBytes) == "null" {
		bodyBytes = []byte(`{"content":[{"type":"text","text":"ok"}]}`)
	}
	failed := result.StatusCode >= http.StatusBadRequest || looksLikeToolError(bodyBytes)
	c.debugf("upstream http tools.call server=%s status=%d failed=%t", upstream.Name, result.StatusCode, failed)
	return InvokeResult{StatusCode: result.StatusCode, Body: bodyBytes, Failed: failed}, nil
}

func (c *Client) listToolsHTTP(ctx context.Context, upstream Upstream) ([]Tool, error) {
	if err := c.ensureHTTPSession(ctx, upstream); err != nil {
		return nil, err
	}
	body, err := json.Marshal(Envelope{JSONRPC: "2.0", ID: json.RawMessage([]byte("1")), Method: "tools/list", Params: mustRawJSON(map[string]any{})})
	if err != nil {
		return nil, err
	}
	result, err := c.doHTTPEnvelope(ctx, upstream, body, DefaultMCPProtocolVersion)
	if err != nil {
		return nil, err
	}
	var rpcResp rpcResponse
	if err := json.Unmarshal(result.Body, &rpcResp); err != nil {
		return nil, err
	}
	if len(rpcResp.Error) > 0 && string(rpcResp.Error) != "null" {
		return nil, fmt.Errorf("upstream tools/list error: %s", string(rpcResp.Error))
	}
	return decodeToolsListResult(rpcResp.Result)
}

func (c *Client) forwardEnvelopeHTTP(ctx context.Context, upstream Upstream, envelope Envelope, protocolVersion string) (ForwardResult, error) {
	// "initialize" requests carry no prior session; for everything else,
	// make sure we have one so spec-compliant upstreams don't reject us.
	if envelope.Method != "initialize" {
		if err := c.ensureHTTPSession(ctx, upstream); err != nil {
			return ForwardResult{}, err
		}
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return ForwardResult{}, err
	}
	return c.doHTTPEnvelope(ctx, upstream, body, protocolVersion)
}

func (c *Client) doHTTPEnvelopeRaw(ctx context.Context, upstream Upstream, body []byte, protocolVersion, sessionID string) (*http.Response, error) {
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
	if strings.TrimSpace(protocolVersion) != "" {
		req.Header.Set("MCP-Protocol-Version", protocolVersion)
	}
	if strings.TrimSpace(sessionID) != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	applyAuthHeaders(req, upstream)
	return client.Do(req)
}

func (c *Client) doHTTPEnvelope(ctx context.Context, upstream Upstream, body []byte, protocolVersion string) (ForwardResult, error) {
	sessionID := c.getSession(upstream.Name)
	resp, err := c.doHTTPEnvelopeRaw(ctx, upstream, body, protocolVersion, sessionID)
	if err != nil {
		return ForwardResult{}, err
	}
	defer resp.Body.Close()
	// Capture/clear session id based on response. 404 with an existing
	// session means the server forgot us; drop it so the next call inits.
	if newSession := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id")); newSession != "" {
		c.setSession(upstream.Name, newSession)
	} else if resp.StatusCode == http.StatusNotFound && sessionID != "" {
		c.clearSession(upstream.Name)
	}
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") {
		data, sseErr := extractFirstSSEData(resp.Body)
		if sseErr != nil {
			return ForwardResult{}, sseErr
		}
		return ForwardResult{StatusCode: resp.StatusCode, Body: data, ContentType: "application/json", ProtocolVersion: resp.Header.Get("MCP-Protocol-Version")}, nil
	}
	respBody := new(bytes.Buffer)
	_, err = respBody.ReadFrom(resp.Body)
	if err != nil {
		return ForwardResult{}, err
	}
	return ForwardResult{StatusCode: resp.StatusCode, Body: respBody.Bytes(), ContentType: contentType, ProtocolVersion: resp.Header.Get("MCP-Protocol-Version")}, nil
}

func (c *Client) getSession(name string) string {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	return c.sessions[name]
}

func (c *Client) setSession(name, id string) {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	c.sessions[name] = id
}

func (c *Client) clearSession(name string) {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	delete(c.sessions, name)
}

// ensureHTTPSession initializes the upstream session if not yet established,
// returning silently if the upstream is stateless (doesn't emit
// Mcp-Session-Id). Required by spec-compliant servers (e.g. Shortcut) which
// reject non-initialize requests that arrive without a session id.
func (c *Client) ensureHTTPSession(ctx context.Context, upstream Upstream) error {
	if c.getSession(upstream.Name) != "" {
		return nil
	}
	initBody, err := json.Marshal(Envelope{
		JSONRPC: "2.0",
		ID:      json.RawMessage([]byte("1")),
		Method:  "initialize",
		Params: mustRawJSON(map[string]any{
			"protocolVersion": DefaultMCPProtocolVersion,
			"clientInfo":      map[string]any{"name": "atryum", "version": "0.1.0"},
			"capabilities":    map[string]any{},
		}),
	})
	if err != nil {
		return err
	}
	// doHTTPEnvelope reads the Mcp-Session-Id response header and stores it.
	if _, err := c.doHTTPEnvelope(ctx, upstream, initBody, DefaultMCPProtocolVersion); err != nil {
		return err
	}
	// Spec requires the client to send notifications/initialized after a
	// successful initialize. Stateless servers ignore it; stateful ones need
	// it to mark the session usable.
	if c.getSession(upstream.Name) != "" {
		notifyBody, err := json.Marshal(Envelope{JSONRPC: "2.0", Method: "notifications/initialized", Params: mustRawJSON(map[string]any{})})
		if err == nil {
			_, _ = c.doHTTPEnvelope(ctx, upstream, notifyBody, DefaultMCPProtocolVersion)
		}
	}
	return nil
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
		"protocolVersion": DefaultMCPProtocolVersion,
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
		"protocolVersion": DefaultMCPProtocolVersion,
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
		Name:              u.Name,
		Mode:              mode,
		BaseURL:           strings.TrimRight(u.BaseURL, "/"),
		AuthToken:         u.AuthToken,
		Timeout:           time.Duration(u.TimeoutSeconds) * time.Second,
		Command:           u.Command,
		Args:              append([]string(nil), u.Args...),
		Env:               cloneMap(u.Env),
		Enabled:           u.Enabled,
		OAuthClientID:     strings.TrimSpace(u.OAuthClientID),
		OAuthClientSecret: u.OAuthClientSecret,
		OAuthAuthorizeURL: strings.TrimSpace(u.OAuthAuthorizeURL),
		OAuthTokenURL:     strings.TrimSpace(u.OAuthTokenURL),
		OAuthScopes:       strings.TrimSpace(u.OAuthScopes),
	}
	if strings.TrimSpace(u.OAuthClientID) != "" {
		// Bootstrapped pre-shared client — record provenance so the UI
		// can label it and so subsequent saves don't re-flip provenance.
		upstream.OAuthClientRegistration = ClientRegistrationPreshared
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
		if strings.TrimSpace(upstream.OAuthAuthorizeURL) != "" && strings.TrimSpace(upstream.OAuthTokenURL) != "" {
			return AuthTypeHosted
		}
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
		if strings.TrimSpace(upstream.OAuthAuthorizeURL) != "" && strings.TrimSpace(upstream.OAuthTokenURL) != "" {
			return AuthStatusMissingCredentials
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
	if strings.TrimSpace(upstream.OAuthAuthorizeURL) != "" && strings.TrimSpace(upstream.OAuthTokenURL) != "" && authStatus != AuthStatusReady {
		message := "connect this server to complete OAuth"
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
	if strings.TrimSpace(upstream.OAuthAuthorizeURL) != "" && strings.TrimSpace(upstream.AuthToken) == "" {
		message := "oauth connection required"
		action := "connect this server"
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusNeedsAttention, AuthStatus: AuthStatusMissingCredentials, ReauthNeeded: false, LastCheckOK: false, LastErrorSummary: &message, ActionRequired: &action}
	}
	initBody, err := json.Marshal(Envelope{JSONRPC: "2.0", ID: json.RawMessage([]byte("1")), Method: "initialize", Params: mustRawJSON(map[string]any{
		"protocolVersion": DefaultMCPProtocolVersion,
		"clientInfo":      map[string]any{"name": "atryum", "version": "0.1.0"},
		"capabilities":    map[string]any{},
	})})
	if err != nil {
		message := err.Error()
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusNeedsAttention, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message}
	}
	result, err := c.doHTTPEnvelope(ctx, upstream, initBody, DefaultMCPProtocolVersion)
	if err != nil {
		message := err.Error()
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusUnreachable, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message}
	}
	if result.StatusCode == http.StatusUnauthorized || result.StatusCode == http.StatusForbidden {
		message := fmt.Sprintf("http %d", result.StatusCode)
		if detail := extractErrorDetail(result.Body); detail != "" {
			message = fmt.Sprintf("http %d: %s", result.StatusCode, detail)
		}
		action := "refresh or reconnect credentials"
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusNeedsAttention, AuthStatus: AuthStatusInvalid, ReauthNeeded: true, LastCheckOK: false, LastErrorSummary: &message, ActionRequired: &action}
	}
	if result.StatusCode >= http.StatusBadRequest {
		message := fmt.Sprintf("http %d", result.StatusCode)
		if detail := extractErrorDetail(result.Body); detail != "" {
			message = fmt.Sprintf("http %d: %s", result.StatusCode, detail)
		}
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusDegraded, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message}
	}
	message := fmt.Sprintf("http %d", result.StatusCode)
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
		"protocolVersion": DefaultMCPProtocolVersion,
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

func extractFirstSSEData(r io.Reader) ([]byte, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			return []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("no data in SSE stream")
}

func extractErrorDetail(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var rpcResp struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcResp); err == nil && rpcResp.Error != nil && rpcResp.Error.Message != "" {
		return rpcResp.Error.Message
	}
	var plain struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &plain); err == nil && plain.Message != "" {
		return plain.Message
	}
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) > 200 {
		trimmed = trimmed[:200] + "..."
	}
	return trimmed
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

func applyAuthHeaders(req *http.Request, upstream Upstream) {
	if strings.TrimSpace(upstream.AuthToken) != "" {
		req.Header.Set("Authorization", "Bearer "+upstream.AuthToken)
	}
	for _, header := range upstream.AuthHeaders {
		name := strings.TrimSpace(header.Name)
		value := strings.TrimSpace(header.Value)
		if name == "" || value == "" {
			continue
		}
		req.Header.Set(name, value)
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

func mustRawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (c *Client) debugf(format string, args ...any) {
	if !c.debug {
		return
	}
	log.Printf("[mcp] "+format, args...)
}

func truncateForLog(b []byte, max int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
