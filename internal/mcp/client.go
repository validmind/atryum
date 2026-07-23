package mcp

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/validmind/atryum/internal/config"
	"github.com/validmind/atryum/internal/version"
)

type UpstreamMode string

const (
	UpstreamModeHTTP  UpstreamMode = "http"
	UpstreamModeStdio UpstreamMode = "stdio"
)

const (
	MCPProtocolVersion2024       = "2024-11-05"
	MCPProtocolVersion2025_03_26 = "2025-03-26"
	MCPProtocolVersion2025_06_18 = "2025-06-18"
	MCPProtocolVersion2025       = "2025-11-25"
	DefaultMCPProtocolVersion    = MCPProtocolVersion2025
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
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Upstream struct {
	Name               string
	EndpointSlug       string
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
	GetServerByEndpointSlug(ctx context.Context, slug string) (Upstream, error)
	ListServers(ctx context.Context, filter ServerFilter) ([]Upstream, int, error)
	CountServers(ctx context.Context) (int, error)
	CreateServer(ctx context.Context, upstream Upstream) error
}

type ServerFilter struct {
	Offset  uint64
	Limit   uint64
	Enabled *bool
}

var endpointSlugInvalidChars = regexp.MustCompile(`[^a-z0-9._~-]+`)

func EndpointSlug(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = endpointSlugInvalidChars.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if strings.Trim(slug, ".") == "" {
		return ""
	}
	return slug
}

// CredentialStore returns the OAuth access token currently stored for a
// server. It is consulted on every ResolveContext so that proxied MCP
// requests (tools/list, tools/call, forward) get the live access token
// attached. The interface is intentionally narrow — the resolver does not
// need to know about refresh tokens or expiry; that's the credential
// store's problem. The upstream is passed so the store can refresh with the
// correct token endpoint/client credentials when needed.
type CredentialStore interface {
	GetCredential(ctx context.Context, upstream Upstream) (AccessTokenView, error)
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
	// re-inits. The protocol version is kept with the session because
	// fallback may initialize older servers with 2025-03-26 while Atryum's
	// default remains newer. Stateless upstreams (those that never return
	// the header) stay as empty strings.
	sessionMu        sync.Mutex
	sessionInitLocks map[string]*sync.Mutex
	sessions         map[string]string
	sessionProtocols map[string]string

	// standaloneStreams holds one shared standalone SSE GET per upstream
	// session. Including the session ID in the key prevents a renewed session
	// from reusing the dead connection associated with its predecessor.
	standaloneMu      sync.Mutex
	standaloneStreams map[standaloneStreamKey]*standaloneStream
}

type InvokeResult struct {
	StatusCode int
	Body       []byte
	Failed     bool
}

// ErrStreamTimeout marks an error returned by InvokeStream as caused by the
// header/idle/max-duration bound in StreamOptions firing, as opposed to a
// transport failure or the sink itself returning an error. Callers can
// distinguish it with errors.Is(err, ErrStreamTimeout).
var ErrStreamTimeout = errors.New("stream timeout")

// ErrStreamSessionRetryRefused marks an error returned by InvokeStream as
// caused by an upstream reporting a missing/expired session after events
// had already been relayed for this call. The normal missing-session
// recovery (reinitialize and retry) is only safe when nothing has reached
// the sink yet, since a retry after that point would relay a second copy
// of everything already delivered — so InvokeStream fails instead.
var ErrStreamSessionRetryRefused = errors.New("stream session retry refused: events already relayed")

type ForwardResult struct {
	StatusCode      int
	Body            []byte
	ContentType     string
	ProtocolVersion string
	SessionExpired  bool
	SessionID       string
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
	return &Client{httpClient: &http.Client{}, debug: debug, sessionInitLocks: make(map[string]*sync.Mutex), sessions: make(map[string]string), sessionProtocols: make(map[string]string), standaloneStreams: make(map[standaloneStreamKey]*standaloneStream)}
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
		if slug := EndpointSlug(name); slug != "" {
			upstream, err := r.store.GetServerByEndpointSlug(ctx, slug)
			if err == nil {
				return r.overlayCredential(ctx, upstream), nil
			}
			if err != nil && err != sql.ErrNoRows {
				return Upstream{}, err
			}
		}
	}
	upstream, ok := r.bootstrap[name]
	if !ok || !upstream.Enabled {
		slug := EndpointSlug(name)
		for _, candidate := range r.bootstrap {
			if candidate.Enabled && EndpointSlug(candidate.Name) == slug {
				return r.overlayCredential(ctx, candidate), nil
			}
		}
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
	cred, err := r.credentials.GetCredential(ctx, upstream)
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

// Invoke calls tool on upstream. meta carries the agent's own MCP
// params._meta (e.g. progressToken) so upstreams that support progress
// notifications can correlate them back to the agent's original request.
func (c *Client) Invoke(ctx context.Context, upstream Upstream, tool string, input map[string]any, requestID *string, meta map[string]any) (InvokeResult, error) {
	started := time.Now()
	defer func() {
		c.debugf("upstream invoke transport=%s server=%s tool=%s duration_ms=%d", upstream.Mode, upstream.Name, tool, time.Since(started).Milliseconds())
	}()
	switch upstream.Mode {
	case UpstreamModeStdio:
		return c.invokeStdio(ctx, upstream, tool, input, requestID, meta)
	case UpstreamModeHTTP, "":
		return c.invokeHTTP(ctx, upstream, tool, input, requestID, meta)
	default:
		return InvokeResult{}, fmt.Errorf("unsupported upstream mode %q", upstream.Mode)
	}
}

// mergeRequestMeta merges the agent-supplied params._meta with the
// atryumRequestId Atryum injects for its own audit correlation. Caller keys
// (e.g. progressToken) are preserved; atryumRequestId always reflects the
// current request, overriding any caller-supplied value under that key.
// Returns nil when there is nothing to send, so callers can omit `_meta`
// entirely rather than sending `{}`.
func mergeRequestMeta(meta map[string]any, requestID *string) map[string]any {
	hasRequestID := requestID != nil && *requestID != ""
	if len(meta) == 0 && !hasRequestID {
		return nil
	}
	merged := make(map[string]any, len(meta)+1)
	for k, v := range meta {
		merged[k] = v
	}
	if hasRequestID {
		merged["atryumRequestId"] = *requestID
	}
	return merged
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
	return parseOAuthTokenResponse(resp.StatusCode, bodyBytes, "oauth token exchange")
}

func (c *Client) RefreshOAuthToken(ctx context.Context, upstream Upstream, refreshToken string) (OAuthToken, error) {
	if strings.TrimSpace(upstream.OAuthTokenURL) == "" {
		return OAuthToken{}, fmt.Errorf("oauth_token_url is required")
	}
	if strings.TrimSpace(upstream.OAuthClientID) == "" {
		return OAuthToken{}, fmt.Errorf("oauth client id is required")
	}
	if strings.TrimSpace(refreshToken) == "" {
		return OAuthToken{}, fmt.Errorf("oauth refresh token is required")
	}
	values := url.Values{}
	values.Set("grant_type", "refresh_token")
	values.Set("refresh_token", refreshToken)
	values.Set("client_id", upstream.OAuthClientID)
	if strings.TrimSpace(upstream.OAuthClientSecret) != "" {
		values.Set("client_secret", upstream.OAuthClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.OAuthTokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return OAuthToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	c.debugf("oauth token refresh server=%s token_url=%s client_id=%s has_secret=%t", upstream.Name, upstream.OAuthTokenURL, upstream.OAuthClientID, strings.TrimSpace(upstream.OAuthClientSecret) != "")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.debugf("oauth token refresh transport error server=%s err=%v", upstream.Name, err)
		return OAuthToken{}, err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	c.debugf("oauth token refresh response server=%s status=%d body=%s", upstream.Name, resp.StatusCode, truncateForLog(bodyBytes, 600))
	return parseOAuthTokenResponse(resp.StatusCode, bodyBytes, "oauth token refresh")
}

func parseOAuthTokenResponse(statusCode int, bodyBytes []byte, operation string) (OAuthToken, error) {
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
	if statusCode >= http.StatusBadRequest || payload.Error != "" || payload.AccessToken == "" {
		// Surface as much of the AS response as we can; the bare status is
		// useless for diagnosing why an OAuth token request failed.
		snippet := strings.TrimSpace(string(bodyBytes))
		if len(snippet) > 400 {
			snippet = snippet[:400] + "…"
		}
		switch {
		case payload.Error != "" && payload.ErrorDescription != "":
			return OAuthToken{}, fmt.Errorf("%s failed (status %d): %s — %s", operation, statusCode, payload.Error, payload.ErrorDescription)
		case payload.Error != "":
			return OAuthToken{}, fmt.Errorf("%s failed (status %d): %s", operation, statusCode, payload.Error)
		case snippet != "":
			return OAuthToken{}, fmt.Errorf("%s failed (status %d): %s", operation, statusCode, snippet)
		default:
			return OAuthToken{}, fmt.Errorf("%s failed with status %d (empty body)", operation, statusCode)
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
	started := time.Now()
	c.debugf("connection test start server=%s transport=%s enabled=%t target=%s auth=%s timeout_ms=%d", upstream.Name, upstream.Mode, upstream.Enabled, debugTarget(upstream), debugAuthSummary(upstream), upstream.Timeout.Milliseconds())
	var result ConnectionTestResult
	if !upstream.Enabled {
		message := "server is disabled"
		action := "enable the server before using it"
		result = ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusDisabled, AuthStatus: upstream.Status.AuthStatus, ReauthNeeded: false, LastCheckOK: false, LastErrorSummary: stringPtr(message), ActionRequired: &action}
		c.debugConnectionTestResult(upstream, result, started)
		return result
	}
	switch upstream.Mode {
	case UpstreamModeHTTP:
		result = c.testHTTP(ctx, upstream)
	case UpstreamModeStdio:
		result = c.testStdio(ctx, upstream)
	default:
		message := fmt.Sprintf("unsupported mode %q", upstream.Mode)
		result = ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusNeedsAttention, AuthStatus: AuthStatusUnknown, ReauthNeeded: false, LastCheckOK: false, LastErrorSummary: stringPtr(message)}
	}
	c.debugConnectionTestResult(upstream, result, started)
	return result
}

// marshalToolCallEnvelope builds the JSON-RPC tools/call request body Atryum
// sends upstream. The envelope always uses id "1" — Atryum's own request id,
// if any, travels only in the caller-facing InvocationResponse, not on the
// wire to the upstream.
func marshalToolCallEnvelope(tool string, input map[string]any, requestID *string, meta map[string]any) ([]byte, error) {
	return marshalToolCallEnvelopeWithMeta(tool, input, mergeRequestMeta(meta, requestID))
}

// marshalToolCallEnvelopeWithMeta builds the tools/call request body from an
// already-fully-merged _meta map (see mergeRequestMeta), skipping that merge
// step. Used by the streaming path, which may need to rewrite a caller's
// progressToken after merging but before marshaling (see rewriteProgressToken).
func marshalToolCallEnvelopeWithMeta(tool string, input map[string]any, meta map[string]any) ([]byte, error) {
	params := map[string]any{"name": tool, "arguments": input}
	if meta != nil {
		params["_meta"] = meta
	}
	return json.Marshal(Envelope{JSONRPC: "2.0", ID: json.RawMessage([]byte("1")), Method: "tools/call", Params: mustRawJSON(params)})
}

// toolCallResultFromRPCResponse maps an already-decoded tools/call JSON-RPC
// response to the InvokeResult contract, applying the "ok" fallback body
// when the upstream returns an empty/null result. The second return value
// flags a missing-session RPC error so the caller can decide whether to
// reinitialize and retry.
func toolCallResultFromRPCResponse(rpcResp rpcResponse, statusCode int) (InvokeResult, bool) {
	if len(rpcResp.Error) > 0 && string(rpcResp.Error) != "null" {
		return InvokeResult{StatusCode: statusCode, Body: rpcResp.Error, Failed: true}, isMissingSessionRPCError(rpcResp.Error)
	}
	bodyBytes := rpcResp.Result
	if len(bodyBytes) == 0 || string(bodyBytes) == "null" {
		bodyBytes = []byte(`{"content":[{"type":"text","text":"ok"}]}`)
	}
	failed := statusCode >= http.StatusBadRequest || looksLikeToolError(bodyBytes)
	return InvokeResult{StatusCode: statusCode, Body: bodyBytes, Failed: failed}, false
}

// toolCallResultFromForward decodes a raw tools/call ForwardResult (JSON or
// SSE-wrapped) and maps it via toolCallResultFromRPCResponse.
func toolCallResultFromForward(result ForwardResult) (InvokeResult, bool, error) {
	rpcResp, err := decodeRPCResponse(result, json.RawMessage([]byte("1")))
	if err != nil {
		return InvokeResult{}, false, err
	}
	invoke, missingSession := toolCallResultFromRPCResponse(rpcResp, result.StatusCode)
	return invoke, missingSession, nil
}

func (c *Client) invokeHTTP(ctx context.Context, upstream Upstream, tool string, input map[string]any, requestID *string, meta map[string]any) (InvokeResult, error) {
	if err := c.ensureHTTPSession(ctx, upstream); err != nil {
		return InvokeResult{}, err
	}
	body, err := marshalToolCallEnvelope(tool, input, requestID, meta)
	if err != nil {
		return InvokeResult{}, err
	}
	result, err := c.doHTTPEnvelopeWithSessionRetry(ctx, upstream, body, DefaultMCPProtocolVersion)
	if err != nil {
		return InvokeResult{}, err
	}
	invoke, missingSession, err := toolCallResultFromForward(result)
	if err != nil {
		return InvokeResult{}, err
	}
	if missingSession {
		c.debugf("upstream http tools.call missing session server=%s session=%q status=%d error=%s", upstream.Name, result.SessionID, result.StatusCode, truncateForLog(invoke.Body, 600))
		if retryErr := c.reinitializeRequiredHTTPSession(ctx, upstream, result.SessionID); retryErr != nil {
			c.debugf("upstream http tools.call session retry init failed server=%s session=%q err=%v", upstream.Name, result.SessionID, retryErr)
			return InvokeResult{}, retryErr
		}
		result, err = c.doHTTPEnvelopeWithSessionRetry(ctx, upstream, body, DefaultMCPProtocolVersion)
		if err != nil {
			c.debugf("upstream http tools.call session retry transport failed server=%s err=%v", upstream.Name, err)
			return InvokeResult{}, err
		}
		invoke, _, err = toolCallResultFromForward(result)
		if err != nil {
			c.debugf("upstream http tools.call session retry decode failed server=%s status=%d err=%v", upstream.Name, result.StatusCode, err)
			return InvokeResult{}, err
		}
	}
	c.debugf("upstream http tools.call server=%s status=%d failed=%t", upstream.Name, result.StatusCode, invoke.Failed)
	return invoke, nil
}

func (c *Client) listToolsHTTP(ctx context.Context, upstream Upstream) ([]Tool, error) {
	if err := c.ensureHTTPSession(ctx, upstream); err != nil {
		return nil, err
	}
	body, err := json.Marshal(Envelope{JSONRPC: "2.0", ID: json.RawMessage([]byte("1")), Method: "tools/list", Params: mustRawJSON(map[string]any{})})
	if err != nil {
		return nil, err
	}
	result, err := c.doHTTPEnvelopeWithSessionRetry(ctx, upstream, body, DefaultMCPProtocolVersion)
	if err != nil {
		return nil, err
	}
	rpcResp, err := decodeRPCResponse(result, json.RawMessage([]byte("1")))
	if err != nil {
		return nil, err
	}
	if len(rpcResp.Error) > 0 && string(rpcResp.Error) != "null" {
		if isMissingSessionRPCError(rpcResp.Error) {
			if retryErr := c.reinitializeRequiredHTTPSession(ctx, upstream, result.SessionID); retryErr != nil {
				return nil, retryErr
			}
			result, err = c.doHTTPEnvelopeWithSessionRetry(ctx, upstream, body, DefaultMCPProtocolVersion)
			if err != nil {
				return nil, err
			}
			rpcResp, err = decodeRPCResponse(result, json.RawMessage([]byte("1")))
			if err != nil {
				return nil, err
			}
		}
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
	return c.doHTTPEnvelopeWithSessionRetry(ctx, upstream, body, protocolVersion)
}

// doHTTPEnvelopeRaw sends one JSON-RPC envelope and returns the raw HTTP
// response (headers received, body not yet consumed). streaming disables
// the per-call upstream.Timeout wrapper: a streaming call's timing is
// governed entirely by the header/idle/max-duration scheme in
// invokeHTTPStream, not by a single fixed wall-clock budget that would
// include however long the stream stays open.
func (c *Client) doHTTPEnvelopeRaw(ctx context.Context, upstream Upstream, body []byte, protocolVersion, sessionID string, streaming bool) (*http.Response, error) {
	endpoint := strings.TrimRight(upstream.BaseURL, "/")
	client := c.httpClient
	if !streaming && upstream.Timeout > 0 {
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

// httpEnvelopeHeaders is an upstream HTTP response with session bookkeeping
// already applied from its headers, but the body NOT yet consumed. Callers
// own resp.Body: read (or discard) it and close it themselves. This split
// exists so a streaming caller can inspect Content-Type and take over the
// body incrementally instead of buffering it, while the ordinary buffered
// path (doHTTPEnvelope) just reads it in one shot.
type httpEnvelopeHeaders struct {
	resp            *http.Response
	sessionID       string
	sessionExpired  bool
	protocolVersion string
	contentType     string
}

func (c *Client) doHTTPEnvelopeHeaders(ctx context.Context, upstream Upstream, body []byte, protocolVersion string, streaming bool) (httpEnvelopeHeaders, error) {
	sessionID := c.getSession(upstream.Name)
	effectiveProtocol := protocolVersion
	if sessionID != "" {
		if sessionProtocol := c.getSessionProtocol(upstream.Name); sessionProtocol != "" {
			effectiveProtocol = sessionProtocol
		}
	}
	resp, err := c.doHTTPEnvelopeRaw(ctx, upstream, body, effectiveProtocol, sessionID, streaming)
	if err != nil {
		return httpEnvelopeHeaders{}, err
	}
	sessionExpired := false
	// Capture/clear session id based on response. 404 with an existing
	// session means the server forgot us; drop it so the next call inits.
	if newSession := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id")); newSession != "" {
		c.setSession(upstream.Name, newSession, effectiveProtocol)
	} else if resp.StatusCode == http.StatusNotFound && sessionID != "" {
		c.clearSessionIfCurrent(upstream.Name, sessionID)
		sessionExpired = true
	}
	return httpEnvelopeHeaders{
		resp:            resp,
		sessionID:       sessionID,
		sessionExpired:  sessionExpired,
		protocolVersion: resp.Header.Get("MCP-Protocol-Version"),
		contentType:     resp.Header.Get("Content-Type"),
	}, nil
}

func (c *Client) doHTTPEnvelope(ctx context.Context, upstream Upstream, body []byte, protocolVersion string) (ForwardResult, error) {
	h, err := c.doHTTPEnvelopeHeaders(ctx, upstream, body, protocolVersion, false)
	if err != nil {
		return ForwardResult{}, err
	}
	defer h.resp.Body.Close()
	if h.sessionExpired {
		bodyBytes, _ := io.ReadAll(h.resp.Body)
		return ForwardResult{StatusCode: h.resp.StatusCode, Body: bodyBytes, ContentType: h.contentType, ProtocolVersion: h.protocolVersion, SessionExpired: true, SessionID: h.sessionID}, nil
	}
	respBody := new(bytes.Buffer)
	if _, err := respBody.ReadFrom(h.resp.Body); err != nil {
		return ForwardResult{}, err
	}
	return ForwardResult{StatusCode: h.resp.StatusCode, Body: respBody.Bytes(), ContentType: h.contentType, ProtocolVersion: h.protocolVersion, SessionID: h.sessionID}, nil
}

func (c *Client) doHTTPEnvelopeWithSessionRetry(ctx context.Context, upstream Upstream, body []byte, protocolVersion string) (ForwardResult, error) {
	result, err := c.doHTTPEnvelope(ctx, upstream, body, protocolVersion)
	if err != nil || !result.SessionExpired {
		return result, err
	}
	if err := c.reinitializeRequiredHTTPSession(ctx, upstream, result.SessionID); err != nil {
		return ForwardResult{}, err
	}
	return c.doHTTPEnvelope(ctx, upstream, body, protocolVersion)
}

func (c *Client) getSession(name string) string {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	return c.sessions[name]
}

func (c *Client) getSessionInitLock(name string) *sync.Mutex {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	lock := c.sessionInitLocks[name]
	if lock == nil {
		lock = &sync.Mutex{}
		c.sessionInitLocks[name] = lock
	}
	return lock
}

func (c *Client) getSessionProtocol(name string) string {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	return c.sessionProtocols[name]
}

func (c *Client) setSession(name, id, protocolVersion string) {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	c.sessions[name] = id
	c.sessionProtocols[name] = protocolVersion
}

func (c *Client) clearSession(name string) {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	delete(c.sessions, name)
	delete(c.sessionProtocols, name)
}

func (c *Client) clearSessionIfCurrent(name, id string) {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if c.sessions[name] != id {
		return
	}
	delete(c.sessions, name)
	delete(c.sessionProtocols, name)
}

func httpProtocolCandidates() []string {
	return []string{
		DefaultMCPProtocolVersion,
		MCPProtocolVersion2025_06_18,
		MCPProtocolVersion2025_03_26,
		MCPProtocolVersion2024,
	}
}

// ensureHTTPSession initializes the upstream session if not yet established,
// returning silently if the upstream is stateless (doesn't emit
// Mcp-Session-Id). Required by spec-compliant servers (e.g. Shortcut) which
// reject non-initialize requests that arrive without a session id.
func (c *Client) ensureHTTPSession(ctx context.Context, upstream Upstream) error {
	return c.ensureHTTPSessionMode(ctx, upstream, false)
}

func (c *Client) reinitializeRequiredHTTPSession(ctx context.Context, upstream Upstream, failedSessionID string) error {
	initLock := c.getSessionInitLock(upstream.Name)
	initLock.Lock()
	defer initLock.Unlock()

	currentSessionID := c.getSession(upstream.Name)
	if failedSessionID != "" && currentSessionID != "" && currentSessionID != failedSessionID {
		c.debugf("upstream session reinit skipped server=%s failed_session=%q current_session=%q reason=session_already_replaced", upstream.Name, failedSessionID, currentSessionID)
		return nil
	}
	if failedSessionID == "" && currentSessionID != "" {
		c.debugf("upstream session reinit skipped server=%s current_session=%q reason=session_already_exists", upstream.Name, currentSessionID)
		return nil
	}
	if failedSessionID != "" {
		c.debugf("upstream session reinit clearing server=%s session=%q", upstream.Name, failedSessionID)
		c.clearSessionIfCurrent(upstream.Name, failedSessionID)
	} else {
		c.debugf("upstream session reinit clearing server=%s session=(none)", upstream.Name)
		c.clearSession(upstream.Name)
	}
	return c.initializeHTTPSessionCandidates(ctx, upstream, true)
}

func (c *Client) ensureHTTPSessionMode(ctx context.Context, upstream Upstream, requireSession bool) error {
	initLock := c.getSessionInitLock(upstream.Name)
	initLock.Lock()
	defer initLock.Unlock()

	if c.getSession(upstream.Name) != "" {
		return nil
	}
	return c.initializeHTTPSessionCandidates(ctx, upstream, requireSession)
}

func (c *Client) initializeHTTPSessionCandidates(ctx context.Context, upstream Upstream, requireSession bool) error {
	var lastErr error
	for _, protocolVersion := range httpProtocolCandidates() {
		c.debugf("upstream session initialize attempt server=%s protocol=%s require_session=%t", upstream.Name, protocolVersion, requireSession)
		hadSession, err := c.initializeHTTPSession(ctx, upstream, protocolVersion)
		if err != nil {
			lastErr = err
			c.debugf("upstream session initialize failed server=%s protocol=%s err=%v", upstream.Name, protocolVersion, err)
			if isProtocolNegotiationError(err) {
				c.debugf("upstream session initialize trying fallback server=%s rejected_protocol=%s", upstream.Name, protocolVersion)
				continue
			}
			return err
		}
		if !requireSession || hadSession {
			c.debugf("upstream session initialize accepted server=%s protocol=%s had_session=%t", upstream.Name, protocolVersion, hadSession)
			return nil
		}
		lastErr = fmt.Errorf("upstream initialize using MCP %s did not return Mcp-Session-Id", protocolVersion)
		c.debugf("upstream session initialize rejected server=%s protocol=%s reason=missing_session_id", upstream.Name, protocolVersion)
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("upstream initialize failed")
}

func (c *Client) initializeHTTPSession(ctx context.Context, upstream Upstream, protocolVersion string) (bool, error) {
	c.debugf("connection test http initialize server=%s url=%s protocol=%s auth=%s", upstream.Name, upstream.BaseURL, protocolVersion, debugAuthSummary(upstream))
	initBody, err := json.Marshal(Envelope{
		JSONRPC: "2.0",
		ID:      json.RawMessage([]byte("1")),
		Method:  "initialize",
		Params: mustRawJSON(map[string]any{
			"protocolVersion": protocolVersion,
			"clientInfo":      map[string]any{"name": "atryum", "version": version.Version},
			"capabilities":    map[string]any{},
		}),
	})
	if err != nil {
		c.debugf("connection test http marshal error server=%s err=%v", upstream.Name, err)
		return false, err
	}
	// doHTTPEnvelope reads the Mcp-Session-Id response header and stores it.
	result, err := c.doHTTPEnvelope(ctx, upstream, initBody, protocolVersion)
	if err != nil {
		c.debugf("connection test http transport error server=%s err=%v", upstream.Name, err)
		return false, err
	}
	c.debugf("connection test http response server=%s status=%d content_type=%q protocol=%q body=%s", upstream.Name, result.StatusCode, result.ContentType, result.ProtocolVersion, truncateForLog(result.Body, 600))
	if result.StatusCode >= http.StatusBadRequest {
		c.clearSession(upstream.Name)
		return false, fmt.Errorf("upstream initialize using MCP %s failed: http %d: %s", protocolVersion, result.StatusCode, extractForwardResultErrorDetail(result, json.RawMessage([]byte("1"))))
	}
	resultBody, err := DecodeJSONRPCPayload(result, json.RawMessage([]byte("1")))
	if err != nil {
		c.clearSession(upstream.Name)
		return false, fmt.Errorf("upstream initialize using MCP %s returned invalid JSON-RPC: %w", protocolVersion, err)
	}
	if len(bytes.TrimSpace(resultBody)) > 0 {
		var rpcResp rpcResponse
		if err := json.Unmarshal(resultBody, &rpcResp); err != nil {
			c.clearSession(upstream.Name)
			return false, fmt.Errorf("upstream initialize using MCP %s returned invalid JSON-RPC: %w", protocolVersion, err)
		}
		if len(rpcResp.Error) > 0 && string(rpcResp.Error) != "null" {
			c.clearSession(upstream.Name)
			return false, fmt.Errorf("upstream initialize using MCP %s error: %s", protocolVersion, rpcErrorDetail(rpcResp.Error))
		}
	}
	// Spec requires the client to send notifications/initialized after a
	// successful initialize. Stateless servers ignore it; stateful ones need
	// it to mark the session usable.
	hadSession := c.getSession(upstream.Name) != ""
	if hadSession {
		notifyBody, err := json.Marshal(Envelope{JSONRPC: "2.0", Method: "notifications/initialized", Params: mustRawJSON(map[string]any{})})
		if err == nil {
			_, _ = c.doHTTPEnvelope(ctx, upstream, notifyBody, protocolVersion)
		}
	}
	return hadSession, nil
}

func decodeRPCResponse(result ForwardResult, expectedID json.RawMessage) (rpcResponse, error) {
	body, err := DecodeJSONRPCPayload(result, expectedID)
	if err != nil {
		return rpcResponse{}, err
	}
	var rpcResp rpcResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return rpcResponse{}, err
	}
	return rpcResp, nil
}

// DecodeJSONRPCPayload returns the JSON-RPC response payload from a raw
// upstream transport result. Plain JSON bodies are already one payload; SSE
// bodies are scanned for the response event matching expectedID, skipping
// notifications and joining multi-line data fields according to the SSE spec.
func DecodeJSONRPCPayload(result ForwardResult, expectedID json.RawMessage) ([]byte, error) {
	if strings.Contains(strings.ToLower(result.ContentType), "text/event-stream") {
		return extractSSEJSONRPCResponse(bytes.NewReader(result.Body), expectedID)
	}
	return result.Body, nil
}

func extractForwardResultErrorDetail(result ForwardResult, expectedID json.RawMessage) string {
	if body, err := DecodeJSONRPCPayload(result, expectedID); err == nil && len(bytes.TrimSpace(body)) > 0 {
		return extractErrorDetail(body)
	}
	return extractErrorDetail(result.Body)
}

// stdioStderrCap bounds how much of a stdio subprocess's stderr is
// retained in memory for error diagnostics. Streaming calls can run far
// longer than the old fixed request_timeout_seconds bound (up to
// stream_max_duration_seconds, or unlimited if unset), so a verbose or
// misbehaving upstream writing continuously to stderr for the life of a
// long relay could otherwise grow this buffer without limit.
const stdioStderrCap = 64 * 1024

// boundedBuffer caps how many bytes Write retains, keeping the first
// stdioStderrCap bytes and silently discarding the rest — matching this
// file's existing truncateForLog convention (keep the head, not the tail)
// for bounding diagnostic text. Write always reports success for the full
// input, including the discarded portion: the subprocess's stderr pipe
// must never see a short write or an error from this side.
type boundedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	keep := p
	if len(keep) > remaining {
		keep = keep[:remaining]
	}
	if _, err := b.buf.Write(keep); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (b *boundedBuffer) Len() int       { return b.buf.Len() }
func (b *boundedBuffer) String() string { return b.buf.String() }

func (c *Client) invokeStdio(ctx context.Context, upstream Upstream, tool string, input map[string]any, requestID *string, meta map[string]any) (InvokeResult, error) {
	if upstream.Command == "" {
		return InvokeResult{}, fmt.Errorf("stdio upstream %q missing command", upstream.Name)
	}
	cmd := exec.CommandContext(ctx, upstream.Command, upstream.Args...)
	cmd.Env = os.Environ()
	for k, v := range upstream.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	configureStdioProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return InvokeResult{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return InvokeResult{}, err
	}
	stderr := newBoundedBuffer(stdioStderrCap)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return InvokeResult{}, err
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	reader := bufio.NewReader(stdout)
	initID := c.nextRPCID()
	if err := writeRPC(stdin, initID, "initialize", map[string]any{
		"protocolVersion": DefaultMCPProtocolVersion,
		"clientInfo":      map[string]any{"name": "atryum", "version": version.Version},
		"capabilities":    map[string]any{},
	}); err != nil {
		return InvokeResult{}, err
	}
	if _, err := readRPC(reader, rpcIDMessage(initID)); err != nil {
		return InvokeResult{}, err
	}
	_ = writeRPC(stdin, c.nextRPCID(), "notifications/initialized", map[string]any{})
	callParams := map[string]any{"name": tool, "arguments": input}
	if merged := mergeRequestMeta(meta, requestID); merged != nil {
		callParams["_meta"] = merged
	}
	callID := c.nextRPCID()
	if err := writeRPC(stdin, callID, "tools/call", callParams); err != nil {
		return InvokeResult{}, err
	}
	resp, err := readRPC(reader, rpcIDMessage(callID))
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
	configureStdioProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := newBoundedBuffer(stdioStderrCap)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()
	reader := bufio.NewReader(stdout)
	initID := c.nextRPCID()
	if err := writeRPC(stdin, initID, "initialize", map[string]any{
		"protocolVersion": DefaultMCPProtocolVersion,
		"clientInfo":      map[string]any{"name": "atryum", "version": version.Version},
		"capabilities":    map[string]any{},
	}); err != nil {
		return nil, err
	}
	if _, err := readRPC(reader, rpcIDMessage(initID)); err != nil {
		return nil, err
	}
	_ = writeRPC(stdin, c.nextRPCID(), "notifications/initialized", map[string]any{})
	listID := c.nextRPCID()
	if err := writeRPC(stdin, listID, "tools/list", map[string]any{}); err != nil {
		return nil, err
	}
	resp, err := readRPC(reader, rpcIDMessage(listID))
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
		c.debugf("connection test http validation server=%s err=%q action=%q", upstream.Name, message, action)
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusNeedsAttention, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message, ActionRequired: &action}
	}
	if strings.TrimSpace(upstream.OAuthAuthorizeURL) != "" && strings.TrimSpace(upstream.AuthToken) == "" {
		message := "oauth connection required"
		action := "connect this server"
		c.debugf("connection test http validation server=%s err=%q action=%q oauth_authorize_url=%s oauth_token_url=%s", upstream.Name, message, action, upstream.OAuthAuthorizeURL, upstream.OAuthTokenURL)
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusNeedsAttention, AuthStatus: AuthStatusMissingCredentials, ReauthNeeded: false, LastCheckOK: false, LastErrorSummary: &message, ActionRequired: &action}
	}
	if err := c.ensureHTTPSession(ctx, upstream); err != nil {
		message := err.Error()
		switch {
		case strings.Contains(message, "http 401"), strings.Contains(message, "http 403"):
			action := "refresh or reconnect credentials"
			return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusNeedsAttention, AuthStatus: AuthStatusInvalid, ReauthNeeded: true, LastCheckOK: false, LastErrorSummary: &message, ActionRequired: &action}
		case strings.Contains(message, "http "):
			return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusDegraded, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message}
		default:
			return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusUnreachable, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message}
		}
	}
	message := "http initialize ok"
	return ConnectionTestResult{Ok: true, Message: message, ConnectionStatus: ConnectionStatusReady, AuthStatus: AuthStatusReady, ReauthNeeded: false, LastCheckOK: true}
}

func (c *Client) testStdio(ctx context.Context, upstream Upstream) ConnectionTestResult {
	if strings.TrimSpace(upstream.Command) == "" {
		message := "missing command"
		action := "set a command for this stdio server"
		c.debugf("connection test stdio validation server=%s err=%q action=%q", upstream.Name, message, action)
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusNeedsAttention, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message, ActionRequired: &action}
	}
	if strings.Contains(strings.ToLower(upstream.Name), "shortcut") && !hasCredentialEnv(upstream.Env) {
		message := "missing credential environment variable"
		action := "add the required API token env var"
		c.debugf("connection test stdio validation server=%s err=%q action=%q env_keys=%s", upstream.Name, message, action, strings.Join(debugMapKeys(upstream.Env), ","))
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusNeedsAttention, AuthStatus: AuthStatusMissingCredentials, LastCheckOK: false, LastErrorSummary: &message, ActionRequired: &action}
	}
	c.debugf("connection test stdio initialize server=%s command=%q args=%s env_keys=%s", upstream.Name, upstream.Command, strings.Join(upstream.Args, " "), strings.Join(debugMapKeys(upstream.Env), ","))
	cmd := exec.CommandContext(ctx, upstream.Command, upstream.Args...)
	cmd.Env = os.Environ()
	for k, v := range upstream.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	configureStdioProcessGroup(cmd)
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
	stderr := newBoundedBuffer(stdioStderrCap)
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
	initID := c.nextRPCID()
	if err := writeRPC(stdin, initID, "initialize", map[string]any{
		"protocolVersion": DefaultMCPProtocolVersion,
		"clientInfo":      map[string]any{"name": "atryum", "version": version.Version},
		"capabilities":    map[string]any{},
	}); err != nil {
		message := err.Error()
		return ConnectionTestResult{Ok: false, Message: message, ConnectionStatus: ConnectionStatusUnreachable, AuthStatus: AuthStatusUnknown, LastCheckOK: false, LastErrorSummary: &message}
	}
	if _, err := readRPC(reader, rpcIDMessage(initID)); err != nil {
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

type jsonRPCResponseMatch int

const (
	jsonRPCResponseNoMatch jsonRPCResponseMatch = iota
	jsonRPCResponseIDMatch
	jsonRPCResponseNullIDError
)

func classifyJSONRPCResponsePayload(payload []byte, expectedID json.RawMessage) jsonRPCResponseMatch {
	var message map[string]json.RawMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return jsonRPCResponseNoMatch
	}
	id, hasID := message["id"]
	if !hasID {
		return jsonRPCResponseNoMatch
	}
	_, hasResult := message["result"]
	_, hasError := message["error"]
	if !hasResult && !hasError {
		return jsonRPCResponseNoMatch
	}
	if hasError && jsonRawIsNull(id) {
		return jsonRPCResponseNullIDError
	}
	if len(bytes.TrimSpace(expectedID)) == 0 {
		return jsonRPCResponseNoMatch
	}
	if jsonRPCIDsMatch(id, expectedID) {
		return jsonRPCResponseIDMatch
	}
	return jsonRPCResponseNoMatch
}

func jsonRPCIDsMatch(a, b json.RawMessage) bool {
	if jsonRawEqual(a, b) {
		return true
	}
	av, aok := jsonIDComparable(a)
	bv, bok := jsonIDComparable(b)
	return aok && bok && av == bv
}

func jsonRawIsNull(raw json.RawMessage) bool {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	}
	return bytes.Equal(compact.Bytes(), []byte("null"))
}

func jsonIDComparable(raw json.RawMessage) (string, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	switch v := value.(type) {
	case string:
		return v, true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	default:
		return "", false
	}
}

func jsonRawEqual(a, b json.RawMessage) bool {
	var compactA bytes.Buffer
	if err := json.Compact(&compactA, a); err != nil {
		return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
	}
	var compactB bytes.Buffer
	if err := json.Compact(&compactB, b); err != nil {
		return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
	}
	return bytes.Equal(compactA.Bytes(), compactB.Bytes())
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

func rpcErrorDetail(body json.RawMessage) string {
	if len(body) == 0 || string(body) == "null" {
		return ""
	}
	var rpcErr struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &rpcErr); err == nil && strings.TrimSpace(rpcErr.Message) != "" {
		if rpcErr.Code != 0 {
			return fmt.Sprintf(`{"code":%d,"message":%q}`, rpcErr.Code, rpcErr.Message)
		}
		return rpcErr.Message
	}
	return strings.TrimSpace(string(body))
}

func isProtocolNegotiationError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "protocol")
}

func isMissingSessionRPCError(body json.RawMessage) bool {
	lower := strings.ToLower(rpcErrorDetail(body))
	return strings.Contains(lower, "session") && strings.Contains(lower, "no session")
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

// readRPC reads newline-delimited JSON-RPC messages from reader until it
// finds the response whose id matches expectedID, skipping everything else
// (notifications, stray server-to-client requests, responses to some other
// id). This replaces a historical "first message with any id/result/error
// wins" read: without correlation, a genuine incoming server-to-client
// request (which also carries an id) could be misread as the answer to our
// own call, since it was indistinguishable from a response by that check.
func readRPC(reader *bufio.Reader, expectedID json.RawMessage) (rpcResponse, error) {
	return readRPCWithLimit(reader, expectedID, defaultStreamMaxMessageBytes)
}

func readRPCWithLimit(reader *bufio.Reader, expectedID json.RawMessage, maxMessageBytes int) (rpcResponse, error) {
	for {
		line, err := readLineLimited(reader, maxMessageBytes)
		if err != nil {
			return rpcResponse{}, err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if classifyRPCMessage(line, expectedID) != rpcMessageTerminalResponse {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		return resp, nil
	}
}

func readLineLimited(reader *bufio.Reader, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = defaultStreamMaxMessageBytes
	}
	line := make([]byte, 0, min(maxBytes, 64*1024))
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > maxBytes-len(line) {
			return nil, ErrStreamMessageTooLarge
		}
		line = append(line, fragment...)
		if err == nil {
			return line, nil
		}
		if err != bufio.ErrBufferFull {
			return nil, err
		}
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

// rpcIDMessage renders a stdio request id (an int64 from nextRPCID) as the
// json.RawMessage form readRPC/classifyRPCMessage compare ids against.
func rpcIDMessage(id int64) json.RawMessage {
	return json.RawMessage(strconv.FormatInt(id, 10))
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

func (c *Client) debugConnectionTestResult(upstream Upstream, result ConnectionTestResult, started time.Time) {
	c.debugf("connection test result server=%s transport=%s ok=%t connection_status=%s auth_status=%s reauth_needed=%t last_check_ok=%t action_required=%q message=%q duration_ms=%d", upstream.Name, upstream.Mode, result.Ok, result.ConnectionStatus, result.AuthStatus, result.ReauthNeeded, result.LastCheckOK, debugStringPtr(result.ActionRequired), result.Message, time.Since(started).Milliseconds())
}

func debugTarget(upstream Upstream) string {
	switch upstream.Mode {
	case UpstreamModeHTTP:
		return upstream.BaseURL
	case UpstreamModeStdio:
		return strings.TrimSpace(strings.Join(append([]string{upstream.Command}, upstream.Args...), " "))
	default:
		return ""
	}
}

func debugAuthSummary(upstream Upstream) string {
	return fmt.Sprintf("has_bearer=%t auth_headers=%s env_credential=%t oauth_provider=%q oauth_client_id=%t oauth_secret=%t oauth_registration=%q",
		strings.TrimSpace(upstream.AuthToken) != "",
		strings.Join(debugAuthHeaderNames(upstream.AuthHeaders), ","),
		hasCredentialEnv(upstream.Env),
		upstream.OAuthProviderID,
		strings.TrimSpace(upstream.OAuthClientID) != "",
		strings.TrimSpace(upstream.OAuthClientSecret) != "",
		upstream.OAuthClientRegistration,
	)
}

func debugAuthHeaderNames(headers []AuthHeader) []string {
	names := make([]string, 0, len(headers))
	for _, header := range headers {
		name := strings.TrimSpace(header.Name)
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func debugMapKeys(in map[string]string) []string {
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func debugStringPtr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func truncateForLog(b []byte, max int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
