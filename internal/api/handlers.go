package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"atryum/internal/auth"
	backendclient "atryum/internal/backend"
	"atryum/internal/invocation"
	"atryum/internal/invocation/policy"
	"atryum/internal/mcp"
	authprovider "atryum/internal/mcp/auth_provider"
	"atryum/internal/store"
)

//go:embed web/*
var webFS embed.FS

type service interface {
	Invoke(ctx context.Context, req invocation.CreateInvocationRequest) (invocation.InvocationResponse, error)
	ListTools(ctx context.Context, server string) ([]mcp.Tool, error)
	ListAllTools(ctx context.Context) ([]mcp.Tool, error)
	ResolveToolServer(ctx context.Context, toolName string) (string, error)
	Get(ctx context.Context, id string) (invocation.InvocationResponse, error)
	List(ctx context.Context, filter invocation.InvocationListFilter) (invocation.InvocationListResponse, error)
	ListAgentIDs(ctx context.Context) ([]string, error)
	Events(ctx context.Context, invocationID string, filter invocation.EventListFilter) (invocation.EventListResponse, error)
	Approve(ctx context.Context, invocationID string) error
	Deny(ctx context.Context, invocationID string, message string) error
	Submit(ctx context.Context, req invocation.ExternalSubmitRequest) (invocation.InvocationResponse, error)
	RecordExecution(ctx context.Context, invocationID string, update invocation.ExternalExecutionUpdate) (invocation.InvocationResponse, error)
	SetSummary(ctx context.Context, invocationID string, summary string) (invocation.InvocationResponse, error)
}

type mcpEnvelopeForwarder interface {
	ForwardEnvelope(ctx context.Context, upstream mcp.Upstream, envelope mcp.Envelope, protocolVersion string) (mcp.ForwardResult, error)
}

type invocationSummarizer interface {
	SummarizeInvocation(ctx context.Context, req backendclient.SummarizeInvocationRequest) (backendclient.SummarizeInvocationResponse, error)
}

type serverService interface {
	List(ctx context.Context, filter mcp.ServerFilter) (ServerListResponse, error)
	Get(ctx context.Context, name string) (AdminServer, error)
	Upsert(ctx context.Context, name string, req AdminServerUpsertRequest) (AdminServer, error)
	Delete(ctx context.Context, name string, disable bool) error
	Test(ctx context.Context, name string) (ServerTestResponse, error)
	StartConnect(ctx context.Context, name string, baseURL string) (OAuthConnectStartResponse, error)
	GetConnectStatus(ctx context.Context, name string) (OAuthConnectStatusResponse, error)
	CompleteConnect(ctx context.Context, state string, code string, errorText string) (OAuthConnectStatusResponse, error)
}

type rulesRepo interface {
	Create(ctx context.Context, rule store.Rule) error
	Get(ctx context.Context, id string) (store.Rule, error)
	List(ctx context.Context) ([]store.Rule, error)
	NextOrder(ctx context.Context) (int, error)
	Update(ctx context.Context, rule store.Rule) error
	Delete(ctx context.Context, id string) error
	Move(ctx context.Context, id string, direction string) ([]store.Rule, error)
	InsertBefore(ctx context.Context, anchorID string, rule store.Rule) error
}

type agentsRepo interface {
	List(ctx context.Context) ([]store.AgentRecord, error)
	ListEnabled(ctx context.Context) ([]store.AgentRecord, error)
	Get(ctx context.Context, id string) (store.AgentRecord, error)
	GetByAgentID(ctx context.Context, agentID string) (store.AgentRecord, error)
	GetByVMCUID(ctx context.Context, vmCUID string) (store.AgentRecord, error)
	UpdateEnabled(ctx context.Context, id string, enabled bool) error
	UpdateAgentIDs(ctx context.Context, id string, agentIDs string) error
	DeleteAll(ctx context.Context) error
}

type agentSyncSettingsRepo interface {
	Get(ctx context.Context) (store.AgentSyncSettings, error)
	Save(ctx context.Context, s store.AgentSyncSettings) error
}

// clientInfoSnapshot is what the agent reported in `initialize.clientInfo`.
type clientInfoSnapshot struct {
	Name    string
	Version string
	At      time.Time
}

type Handler struct {
	svc                   service
	serverSvc             serverService
	policyRegistry        *policy.Registry
	rulesRepo             rulesRepo
	agentsRepo            agentsRepo
	agentSyncSettingsRepo agentSyncSettingsRepo
	backendClient         *backendclient.Client
	summarizeClient       invocationSummarizer
	syncAgentsFn          func(ctx context.Context) error
	forwarder             mcpEnvelopeForwarder
	staticHTTP            http.Handler
	debug                 bool
	authDebugSkip         bool
	authValidator         *auth.Validator
	apiKeyAuth            auth.APIKeyConfig

	// clientInfoCache remembers the most recent `initialize.clientInfo`
	// per MCP session key so that subsequent tools/call requests on the
	// same session can attach client_name / client_version. The key is the
	// `Mcp-Session-Id` header (per MCP streamable HTTP transport); when
	// absent we fall back to remote IP + User-Agent.
	clientInfoMu    sync.RWMutex
	clientInfoCache map[string]clientInfoSnapshot
}

type PolicyStatusResponse struct {
	ActiveProvider string               `json:"active_provider"`
	DisplayName    string               `json:"display_name"`
	Providers      []PolicyProviderInfo `json:"providers"`
}

type PolicyProviderInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type PolicyUpdateRequest struct {
	Provider string `json:"provider"`
}

type ApproveRequest struct {
	CreateRule *AdminRuleInput `json:"create_rule,omitempty"`
}

type DenyRequest struct {
	Message    string          `json:"message,omitempty"`
	CreateRule *AdminRuleInput `json:"create_rule,omitempty"`
}

type AdminServer struct {
	Name                    string            `json:"name"`
	Mode                    string            `json:"mode"`
	BaseURL                 string            `json:"base_url,omitempty"`
	AuthToken               string            `json:"auth_token,omitempty"`
	AuthHeaders             []mcp.AuthHeader  `json:"auth_headers,omitempty"`
	TimeoutSeconds          int               `json:"timeout_seconds"`
	Command                 string            `json:"command,omitempty"`
	Args                    []string          `json:"args,omitempty"`
	Env                     map[string]string `json:"env,omitempty"`
	Enabled                 bool              `json:"enabled"`
	AuthType                string            `json:"auth_type"`
	ConnectionStatus        string            `json:"connection_status"`
	AuthStatus              string            `json:"auth_status"`
	ReauthNeeded            bool              `json:"reauth_needed"`
	LastCheckedAt           *time.Time        `json:"last_checked_at,omitempty"`
	LastCheckOK             bool              `json:"last_check_ok"`
	LastErrorSummary        *string           `json:"last_error_summary,omitempty"`
	ActionRequired          *string           `json:"action_required,omitempty"`
	OAuthProviderID         string            `json:"oauth_provider_id,omitempty"`
	OAuthProviderLabel      string            `json:"oauth_provider_label,omitempty"`
	OAuthClientRegistration string            `json:"oauth_client_registration,omitempty"`
	OAuthClientID           string            `json:"oauth_client_id,omitempty"`
	OAuthAuthorizeURL       string            `json:"oauth_authorize_url,omitempty"`
	OAuthTokenURL           string            `json:"oauth_token_url,omitempty"`
	OAuthScopes             string            `json:"oauth_scopes,omitempty"`
	HasOAuthClientSecret    bool              `json:"has_oauth_client_secret,omitempty"`
	// OAuthGrantedScopes is what the authorization server actually issued
	// on the most recent token exchange (read from oauth_credentials.scope).
	// Distinct from OAuthScopes, which is what we *request* in the next
	// authorize URL. They can diverge: Slack-style ASes honor whatever
	// scopes the registered app declares regardless of what we send.
	OAuthGrantedScopes string `json:"oauth_granted_scopes,omitempty"`
}

type AdminServerUpsertRequest struct {
	Name              string            `json:"name,omitempty"`
	Mode              string            `json:"mode"`
	BaseURL           string            `json:"base_url,omitempty"`
	AuthToken         string            `json:"auth_token,omitempty"`
	AuthHeaders       []mcp.AuthHeader  `json:"auth_headers,omitempty"`
	TimeoutSeconds    int               `json:"timeout_seconds,omitempty"`
	Command           string            `json:"command,omitempty"`
	Args              []string          `json:"args,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	Enabled           *bool             `json:"enabled,omitempty"`
	OAuthClientID     string            `json:"oauth_client_id,omitempty"`
	OAuthClientSecret string            `json:"oauth_client_secret,omitempty"`
	OAuthAuthorizeURL string            `json:"oauth_authorize_url,omitempty"`
	OAuthTokenURL     string            `json:"oauth_token_url,omitempty"`
	OAuthScopes       string            `json:"oauth_scopes,omitempty"`
}

type ServerListResponse struct {
	Items  []AdminServer `json:"items"`
	Total  int           `json:"total"`
	Offset uint64        `json:"offset"`
	Limit  uint64        `json:"limit"`
}

type ServerTestResponse struct {
	Ok               bool       `json:"ok"`
	Message          string     `json:"message"`
	ConnectionStatus string     `json:"connection_status"`
	AuthStatus       string     `json:"auth_status"`
	ReauthNeeded     bool       `json:"reauth_needed"`
	LastCheckedAt    *time.Time `json:"last_checked_at,omitempty"`
	LastCheckOK      bool       `json:"last_check_ok"`
	LastErrorSummary *string    `json:"last_error_summary,omitempty"`
	ActionRequired   *string    `json:"action_required,omitempty"`
}

type OAuthConnectStartResponse struct {
	ConnectURL string `json:"connect_url"`
	State      string `json:"state"`
}

type OAuthConnectStatusResponse struct {
	Status      string     `json:"status"`
	Message     *string    `json:"message,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// ─── Rules ───────────────────────────────────────────────────────────────────

type AdminRule struct {
	ID              string    `json:"id"`
	Action          string    `json:"action"`
	ServerPatterns  []string  `json:"server_patterns"`
	ToolPatterns    []string  `json:"tool_patterns"`
	AgentIDPattern  string    `json:"agent_id_pattern"`
	ModelConfigCUID string    `json:"model_config_cuid,omitempty"`
	AgentCUIDs      []string  `json:"agent_cuids"`
	Description     string    `json:"description,omitempty"`
	Enabled         bool      `json:"enabled"`
	Order           int       `json:"order"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type AdminRuleInput struct {
	Action          string   `json:"action"`
	ServerPatterns  []string `json:"server_patterns"`
	ToolPatterns    []string `json:"tool_patterns"`
	AgentIDPattern  string   `json:"agent_id_pattern"`
	ModelConfigCUID string   `json:"model_config_cuid,omitempty"`
	AgentCUIDs      []string `json:"agent_cuids"`
	Description     string   `json:"description,omitempty"`
	Enabled         *bool    `json:"enabled,omitempty"`
}

// AdminModelConfig is the API representation of a VM agent model configuration.
type AdminModelConfig struct {
	CUID string `json:"cuid"`
	Name string `json:"name"`
}

type ModelConfigListResponse struct {
	Items []AdminModelConfig `json:"items"`
	Total int                `json:"total"`
}

type RuleListResponse struct {
	Items []AdminRule `json:"items"`
}

// ─── Agent admin types ────────────────────────────────────────────────────────

type AdminAgent struct {
	CUID        string    `json:"cuid"`
	OrgName     string    `json:"org_name"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	AgentIDs    []string  `json:"agent_ids"`
	SyncedAt    time.Time `json:"synced_at"`
	Enabled     bool      `json:"enabled"`
}

type AdminAgentInput struct {
	Enabled  bool     `json:"enabled"`
	AgentIDs []string `json:"agent_ids,omitempty"`
}

type AgentListResponse struct {
	Items []AdminAgent `json:"items"`
}

func toAdminAgent(a store.AgentRecord) AdminAgent {
	ids := parseAgentIDs(a.AgentIDs)
	return AdminAgent{
		CUID:        a.ID,
		OrgName:     a.VMOrganizationName,
		Name:        a.VMName,
		Description: a.VMDescription,
		AgentIDs:    ids,
		SyncedAt:    a.SyncedAt,
		Enabled:     a.Enabled,
	}
}

func parseAgentIDs(raw string) []string {
	if raw == "" {
		return []string{}
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return []string{}
	}
	if ids == nil {
		return []string{}
	}
	return ids
}

const agentRulesToolName = "atryum.rules.get"

// atryumInitializeInstructions is returned in the MCP `initialize` result so
// MCP clients can surface it to the model as system-level guidance. The goal
// is to make the agent aware that approval rules govern tool access and that
// it should consult `atryum.rules.get` before choosing tools.
const atryumInitializeInstructions = "This MCP server (atryum) gates tool calls through approval rules. " +
	"Before calling any tool, call the `atryum.rules.get` tool to retrieve the rules that apply to you " +
	"and the effective action (`auto_approve`, `human_approval`, or `auto_deny`) for a given server/tool. " +
	"Prefer tools that resolve to `auto_approve`. Tools that resolve to `human_approval` will block until a " +
	"human responds; tools that resolve to `auto_deny` will be rejected. " +
	"Pass the target `server` (or `source`) and `tool` arguments to `atryum.rules.get` to get a disposition."

type AgentRule struct {
	ID             string   `json:"id"`
	Action         string   `json:"action"`
	ServerPatterns []string `json:"server_patterns"`
	ToolPatterns   []string `json:"tool_patterns"`
	AgentIDPattern string   `json:"agent_id_pattern"`
	Description    string   `json:"description,omitempty"`
	Order          int      `json:"order"`
}

type AgentRulesResponse struct {
	AgentID       string      `json:"agent_id,omitempty"`
	Server        string      `json:"server,omitempty"`
	Tool          string      `json:"tool,omitempty"`
	DefaultAction string      `json:"default_action"`
	Action        string      `json:"action,omitempty"`
	MatchedRuleID *string     `json:"matched_rule_id,omitempty"`
	Items         []AgentRule `json:"items"`
}

var agentRulesToolInputSchema = json.RawMessage(`{"type":"object","properties":{"server":{"type":"string","description":"MCP server/upstream name to evaluate"},"source":{"type":"string","description":"External executor source to evaluate, such as amp"},"tool":{"type":"string","description":"Tool name to evaluate"}},"additionalProperties":false}`)

// ─────────────────────────────────────────────────────────────────────────────

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type invocationStreamEnvelope struct {
	Items []invocation.InvocationResponse `json:"items"`
}

func NewHandler(svc service, serverSvc serverService, policyRegistry *policy.Registry, rules rulesRepo, agents agentsRepo, agentSyncSettings agentSyncSettingsRepo, syncAgents func(ctx context.Context) error, bc *backendclient.Client) *Handler {
	staticSub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	debug := strings.EqualFold(os.Getenv("ATRYUM_MCP_DEBUG"), "1") || strings.EqualFold(os.Getenv("ATRYUM_MCP_DEBUG"), "true")
	var forwarder mcpEnvelopeForwarder
	if f, ok := svc.(mcpEnvelopeForwarder); ok {
		forwarder = f
	}
	return &Handler{svc: svc, serverSvc: serverSvc, policyRegistry: policyRegistry, rulesRepo: rules, agentsRepo: agents, agentSyncSettingsRepo: agentSyncSettings, backendClient: bc, summarizeClient: bc, syncAgentsFn: syncAgents, forwarder: forwarder, staticHTTP: http.FileServer(http.FS(staticSub)), debug: debug, clientInfoCache: make(map[string]clientInfoSnapshot)}
}

// SetAuthValidator installs the inbound auth validator. When non-nil, the
// /mcp/ routes require a valid bearer token. Pass nil (or omit) to leave
// /mcp/ anonymous.
func (h *Handler) SetAuthValidator(v *auth.Validator) {
	h.authValidator = v
}

func (h *Handler) SetAuthDebugSkipVerify(enabled bool) {
	h.authDebugSkip = enabled
}

// SetAPIKeyAuth installs the static api-key/secret pair used to protect the
// read-only invocation reporting endpoints.
func (h *Handler) SetAPIKeyAuth(cfg auth.APIKeyConfig) {
	h.apiKeyAuth = cfg
}

// protectedResourceMetadata serves the OAuth 2.0 protected-resource metadata
// (RFC 9728) document so MCP clients can discover the authorization server.
// The resource URL is computed from the incoming request so deployments
// behind reverse proxies pick up X-Forwarded-* automatically.
func (h *Handler) protectedResourceMetadata() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resource := strings.TrimRight(baseURL(r), "/") + "/mcp"
		auth.ProtectedResourceHandler(h.authValidator, resource).ServeHTTP(w, r)
	})
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.healthz)
	if h.authValidator != nil {
		mux.Handle("/.well-known/oauth-protected-resource", h.protectedResourceMetadata())
	}
	mcpHandler := auth.MiddlewareWithOptions(h.authValidator, "/.well-known/oauth-protected-resource", auth.MiddlewareOptions{SkipVerify: h.authDebugSkip, DebugLogIdentity: h.debug})(http.HandlerFunc(h.invokeUpstream))
	mcpHandler = h.noAuthAgentIDHint(mcpHandler)
	mux.Handle("/mcp/", mcpHandler)
	mux.HandleFunc("/api/v1/invocations", h.invocations)
	mux.HandleFunc("/api/v1/admin/invocations", h.adminInvocations)
	mux.HandleFunc("/api/v1/admin/invocations/stream", h.adminInvocationStream)
	mux.HandleFunc("/api/v1/admin/invocations/", h.adminInvocationDetail)
	mux.HandleFunc("/api/v1/admin/servers", h.adminServers)
	mux.HandleFunc("/api/v1/admin/servers/", h.adminServerDetail)
	mux.HandleFunc("/api/v1/admin/rules", h.adminRules)
	mux.HandleFunc("/api/v1/admin/rules/", h.adminRuleDetail)
	mux.HandleFunc("/api/v1/admin/agents", h.adminAgents)
	mux.HandleFunc("/api/v1/admin/agents/", h.adminAgentDetail)
	mux.HandleFunc("/api/v1/admin/model-configs", h.adminModelConfigs)
	mux.HandleFunc("/api/v1/admin/settings", h.adminSettings)
	mux.HandleFunc("/api/v1/admin/vm/organizations", h.adminVMOrganizations)
	mux.HandleFunc("/api/v1/admin/vm/record-types", h.adminVMRecordTypes)
	mux.HandleFunc("/api/v1/admin/vm/custom-fields", h.adminVMCustomFields)
	mux.HandleFunc("/api/v1/admin/oauth/callback", h.oauthCallback)
	mux.HandleFunc("/api/v1/admin/policy", h.adminPolicy)
	agentRulesHandler := auth.MiddlewareWithOptions(h.authValidator, "/.well-known/oauth-protected-resource", auth.MiddlewareOptions{SkipVerify: h.authDebugSkip, DebugLogIdentity: h.debug})(http.HandlerFunc(h.agentRules))
	agentRulesHandler = h.noAuthAgentIDHint(agentRulesHandler)
	mux.Handle("/api/v1/agent/rules", agentRulesHandler)
	mux.HandleFunc("/api/v1/external/invocations", h.externalInvocations)
	mux.HandleFunc("/api/v1/external/invocations/", h.externalInvocationDetail)
	apiKeyMW := auth.APIKeyMiddleware(h.apiKeyAuth)
	mux.Handle("/agent_ids", apiKeyMW(http.HandlerFunc(h.agentIDs)))
	mux.Handle("/invocations/", apiKeyMW(http.HandlerFunc(h.invocationsByAgentID)))
	mux.Handle("/models/", apiKeyMW(http.HandlerFunc(h.invocationsByVMCUID)))
	mux.HandleFunc("/ui", h.uiIndex)
	mux.Handle("/ui/", http.StripPrefix("/ui/", h.spaFileServer()))
	mux.HandleFunc("/", h.root)
	return mux
}

func (h *Handler) noAuthAgentIDHint(next http.Handler) http.Handler {
	if h.authValidator != nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agentID := normalizeNoAuthAgentID(r.URL.Query().Get("agent_id"))
		if agentID == "" {
			next.ServeHTTP(w, r)
			return
		}
		identity := auth.Identity{
			AgentID: agentID,
			Issuer:  "atryum:no-auth",
			Subject: agentID,
		}
		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
	})
}

func normalizeNoAuthAgentID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 {
		return ""
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '.', '_', ':', '-':
			continue
		default:
			return ""
		}
	}
	return value
}

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) root(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	http.Redirect(w, r, "/ui/", http.StatusFound)
}

func (h *Handler) uiIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ui" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	http.Redirect(w, r, "/ui/", http.StatusFound)
}

// spaFileServer returns a handler that serves static files from the embedded
// web/ directory. If the requested path doesn't match a real file it falls back
// to index.html so the React SPA router can handle the route.
func (h *Handler) spaFileServer() http.Handler {
	staticSub, _ := fs.Sub(webFS, "web")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "" || path == "/" {
			path = "index.html"
		}
		// Try to open the requested file; if it doesn't exist, serve index.html.
		if _, err := fs.Stat(staticSub, path); err != nil {
			r.URL.Path = "/"
		}
		h.staticHTTP.ServeHTTP(w, r)
	})
}

func (h *Handler) invokeUpstream(w http.ResponseWriter, r *http.Request) {
	server := strings.TrimPrefix(r.URL.Path, "/mcp/")
	server = strings.Trim(server, "/")

	// GET: open an SSE keepalive stream (Streamable HTTP transport and legacy SSE clients both try GET)
	if r.Method == http.MethodGet {
		h.handleMCPSSE(w, r)
		return
	}
	if r.Method == http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if isJSONRPCRequest(r) {
		// server may be "" — handleMCPProxy handles the aggregate (no-server) case
		h.handleMCPProxy(w, r, server)
		return
	}
	if server == "" {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	h.handleInvocation(w, r, server)
}

// handleMCPSSE opens a long-lived SSE stream so Cursor's MCP client can establish
// a connection. Actual JSON-RPC exchanges still travel over POST; this stream
// exists purely to satisfy the SSE handshake and sends periodic keepalive pings.
func (h *Handler) handleMCPSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, ": atryum mcp ready\n\n")
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func isJSONRPCRequest(r *http.Request) bool {
	if strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return true
	}
	return true
}

func (h *Handler) invocations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.handleInvocation(w, r, "")
}

func (h *Handler) agentRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.rulesRepo == nil {
		writeJSON(w, http.StatusOK, AgentRulesResponse{DefaultAction: invocation.RuleActionHumanApproval, Items: []AgentRule{}})
		return
	}

	agentID := auth.AgentIDFromContext(r.Context())
	if agentID == "" && h.authValidator == nil {
		agentID = normalizeNoAuthAgentID(r.URL.Query().Get("agent_id"))
	}
	if agentID == "" && h.authValidator == nil {
		agentID = strings.TrimSpace(r.URL.Query().Get("request_id"))
	}
	server := strings.TrimSpace(r.URL.Query().Get("server"))
	if server == "" {
		server = strings.TrimSpace(r.URL.Query().Get("source"))
	}
	tool := strings.TrimSpace(r.URL.Query().Get("tool"))

	resp, err := h.buildAgentRulesResponse(r.Context(), agentID, server, tool)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) buildAgentRulesResponse(ctx context.Context, agentID, server, tool string) (AgentRulesResponse, error) {
	resp := AgentRulesResponse{
		AgentID:       agentID,
		Server:        server,
		Tool:          tool,
		DefaultAction: invocation.RuleActionHumanApproval,
		Items:         []AgentRule{},
	}
	if h.rulesRepo == nil {
		return resp, nil
	}

	rules, err := h.rulesRepo.List(ctx)
	if err != nil {
		return AgentRulesResponse{}, err
	}
	agentCUID := h.resolveAgentRecordForRules(ctx, agentID)

	for _, rule := range rules {
		if !apiRuleMatches(rule, server, tool, agentID, agentCUID, false) {
			continue
		}
		resp.Items = append(resp.Items, AgentRule{
			ID:             rule.ID,
			Action:         rule.Action,
			ServerPatterns: rule.ServerPatterns,
			ToolPatterns:   rule.ToolPatterns,
			AgentIDPattern: rule.AgentIDPattern,
			Description:    rule.Description,
			Order:          rule.Order,
		})
		if resp.Action == "" && server != "" && tool != "" &&
			apiRuleMatches(rule, server, tool, agentID, agentCUID, true) {
			resp.Action = rule.Action
			if rule.ID != "" {
				id := rule.ID
				resp.MatchedRuleID = &id
			}
		}
	}
	return resp, nil
}

func (h *Handler) resolveAgentRecordForRules(ctx context.Context, agentID string) string {
	if h.agentsRepo == nil {
		return ""
	}
	if agentID != "" {
		if rec, err := h.agentsRepo.GetByAgentID(ctx, agentID); err == nil {
			return rec.ID
		}
	}
	if h.agentSyncSettingsRepo == nil {
		return ""
	}
	settings, err := h.agentSyncSettingsRepo.Get(ctx)
	if err != nil {
		return ""
	}
	defaultVMCUID := strings.TrimSpace(settings.DefaultAgentVMCUID)
	if defaultVMCUID == "" {
		return ""
	}
	rec, err := h.agentsRepo.GetByVMCUID(ctx, defaultVMCUID)
	if err != nil {
		return ""
	}
	return rec.ID
}

func apiRuleMatches(rule store.Rule, server, tool, agentID, agentCUID string, includeServerTool bool) bool {
	if !rule.Enabled {
		return false
	}
	if includeServerTool {
		if !apiMatchPatterns(rule.ServerPatterns, server) {
			return false
		}
		if !apiMatchPatterns(rule.ToolPatterns, tool) {
			return false
		}
	}
	if !apiMatchAgentIDPattern(rule.AgentIDPattern, agentID) {
		return false
	}
	return apiMatchAgentCUIDs(rule.AgentCUIDs, agentCUID)
}

func apiMatchAgentCUIDs(cuids []string, agentCUID string) bool {
	if len(cuids) == 0 {
		return true
	}
	for _, cuid := range cuids {
		if cuid == agentCUID {
			return true
		}
	}
	return false
}

func (h *Handler) handleInvocation(w http.ResponseWriter, r *http.Request, server string) {
	var req invocation.CreateInvocationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if server != "" {
		req.Server = server
	}
	if req.RequestID == nil {
		if rid := strings.TrimSpace(r.Header.Get("X-Request-Id")); rid != "" {
			req.RequestID = &rid
		}
	}
	if req.IdempotencyKey == nil {
		if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key != "" {
			req.IdempotencyKey = &key
		}
	}
	resp, err := h.svc.Invoke(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleMCPProxy(w http.ResponseWriter, r *http.Request, server string) {
	started := time.Now()
	var req mcp.Envelope
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeRPCError(w, nil, -32700, "parse error")
		return
	}
	if strings.TrimSpace(req.JSONRPC) == "" {
		req.JSONRPC = "2.0"
	}
	requestID := compactRequestID(req.ID)
	protocolVersion := negotiateProtocolVersion(r.Header.Get("MCP-Protocol-Version"), req.Params)
	h.debugf("server-side mcp request server=%s method=%s id=%s", server, req.Method, requestID)
	defer func() {
		h.debugf("server-side mcp complete server=%s method=%s id=%s duration_ms=%d", server, req.Method, requestID, time.Since(started).Milliseconds())
	}()
	setMCPProtocolVersionHeader(w, protocolVersion)

	switch req.Method {
	case "initialize":
		h.recordInitializeClientInfo(r, req.Params)
		result := map[string]any{
			"protocolVersion": protocolVersion,
			"serverInfo": map[string]any{
				"name":    "atryum",
				"version": "0.1.0",
			},
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"instructions": atryumInitializeInstructions,
		}
		h.writeRPCResult(w, req.ID, result)
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		var tools []mcp.Tool
		var err error
		if server == "" {
			tools, err = h.svc.ListAllTools(r.Context())
		} else {
			tools, err = h.svc.ListTools(r.Context(), server)
		}
		if err != nil {
			h.writeRPCError(w, req.ID, -32000, err.Error())
			return
		}
		tools = appendAtryumTools(tools)
		_ = h.emitTraceEvent(r.Context(), server, "mcp.tools.list", map[string]any{"tool_count": len(tools), "request_id": requestID})
		annotated := h.annotateToolsWithPolicy(r.Context(), server, tools)
		h.writeRPCResult(w, req.ID, map[string]any{"tools": annotated})
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			h.writeRPCError(w, req.ID, -32602, "invalid params")
			return
		}
		if params.Name == agentRulesToolName {
			result, err := h.callAgentRulesTool(r.Context(), server, params.Arguments)
			if err != nil {
				h.writeRPCError(w, req.ID, -32000, err.Error())
				return
			}
			_ = h.emitTraceEvent(r.Context(), server, "mcp.tools.call", map[string]any{"request_id": requestID, "status": "succeeded", "tool": params.Name})
			h.writeRPCResult(w, req.ID, result)
			return
		}
		callServer := server
		if callServer == "" {
			resolved, err := h.svc.ResolveToolServer(r.Context(), params.Name)
			if err != nil {
				h.writeRPCError(w, req.ID, -32000, err.Error())
				return
			}
			callServer = resolved
		}
		toolReq := invocation.CreateInvocationRequest{Server: callServer, Tool: params.Name, Input: params.Arguments}
		if requestID != "" {
			toolReq.RequestID = stringPtr(requestID)
		}
		// Carry forward the harness-reported clientInfo captured at
		// `initialize` time on this same session. Lets the invocations UI
		// show Agent context even when /mcp/ is anonymous (no [[auth]]).
		if snap, ok := h.lookupClientInfo(r); ok {
			toolReq.ClientName = snap.Name
			toolReq.ClientVersion = snap.Version
		}
		resp, err := h.svc.Invoke(r.Context(), toolReq)
		if err != nil {
			h.writeRPCError(w, req.ID, -32000, err.Error())
			return
		}
		tracePayload := map[string]any{"request_id": requestID, "status": resp.Status, "invocation_id": resp.InvocationID, "tool": params.Name}
		_ = h.emitTraceEvent(r.Context(), server, "mcp.tools.call", tracePayload)
		if len(resp.Error) > 0 {
			result := normalizeToolCallResult(resp.Error, true)
			if resp.Status == invocation.StatusDenied {
				result = h.appendRulesContextToToolResult(r.Context(), result, callServer, params.Name)
			}
			h.writeRPCResult(w, req.ID, result)
			return
		}
		h.writeRPCResult(w, req.ID, normalizeToolCallResult(resp.Result, false))
	default:
		forwarded, forwardedOK := h.forwardProxyEnvelope(r.Context(), server, req, protocolVersion)
		if !forwardedOK {
			if req.IsNotification() {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			h.writeRPCError(w, req.ID, -32601, "method not found")
			return
		}
		if forwarded.ProtocolVersion != "" {
			setMCPProtocolVersionHeader(w, forwarded.ProtocolVersion)
		}
		if forwarded.ContentType != "" {
			w.Header().Set("Content-Type", forwarded.ContentType)
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		status := forwarded.StatusCode
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(forwarded.Body)
	}
}

// agentIDs returns the distinct enabled agent IDs configured on synced agents.
// Protected by the API key middleware.
func (h *Handler) agentIDs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.agentsRepo == nil {
		writeError(w, http.StatusServiceUnavailable, "agents repository not configured")
		return
	}
	records, err := h.agentsRepo.ListEnabled(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ids := agentIDsFromRecords(records)
	writeJSON(w, http.StatusOK, map[string]any{"items": ids})
}

func agentIDsFromRecords(records []store.AgentRecord) []string {
	seen := make(map[string]struct{})
	ids := []string{}
	for _, record := range records {
		for _, id := range parseAgentIDs(record.AgentIDs) {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// invocationsByAgentID returns a paginated list of invocations for the given
// agent_id. Supports optional `start_date` and `end_date` query parameters
// (RFC3339). Protected by the API key middleware.
func (h *Handler) invocationsByAgentID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	agentID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/invocations/"), "/")
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	filter := invocation.InvocationListFilter{
		Offset:   readUintQuery(r, "offset", 0),
		Limit:    readUintQuery(r, "limit", 50),
		AgentIDs: []string{agentID},
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("start_date")); raw != "" {
		t, err := parseDateParam(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid start_date: "+err.Error())
			return
		}
		filter.StartDate = &t
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("end_date")); raw != "" {
		t, err := parseDateParam(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid end_date: "+err.Error())
			return
		}
		filter.EndDate = &t
	}
	resp, err := h.svc.List(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// invocationsByVMCUID returns a paginated list of invocations for all runtime
// agent IDs mapped to the given ValidMind inventory model CUID. The vm_cuid is
// taken from the path segment after "/models/" (e.g. "/models/mdl_abc/invocations").
// Protected by the API key middleware.
func (h *Handler) invocationsByVMCUID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Path is /models/{vm_cuid}/invocations
	trimmed := strings.TrimPrefix(r.URL.Path, "/models/")
	vmCUID := strings.TrimSuffix(trimmed, "/invocations")
	vmCUID = strings.Trim(vmCUID, "/")
	if vmCUID == "" {
		writeError(w, http.StatusBadRequest, "vm_cuid is required")
		return
	}
	if h.agentsRepo == nil {
		writeError(w, http.StatusServiceUnavailable, "agents repository not configured")
		return
	}
	record, err := h.agentsRepo.GetByVMCUID(r.Context(), vmCUID)
	if err != nil {
		if err.Error() == "sql: no rows in result set" || strings.Contains(err.Error(), "no rows") {
			writeError(w, http.StatusNotFound, "no agent record found for vm_cuid")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to look up agent record")
		return
	}
	agentIDs := parseAgentIDs(record.AgentIDs)
	if len(agentIDs) == 0 {
		// Agent record exists but has no runtime IDs assigned yet — return empty list.
		limit := readUintQuery(r, "limit", 50)
		writeJSON(w, http.StatusOK, invocation.InvocationListResponse{
			Items:  []invocation.InvocationResponse{},
			Total:  0,
			Offset: readUintQuery(r, "offset", 0),
			Limit:  limit,
		})
		return
	}
	filter := invocation.InvocationListFilter{
		Offset:   readUintQuery(r, "offset", 0),
		Limit:    readUintQuery(r, "limit", 50),
		AgentIDs: agentIDs,
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("start_date")); raw != "" {
		t, err := parseDateParam(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid start_date: "+err.Error())
			return
		}
		filter.StartDate = &t
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("end_date")); raw != "" {
		t, err := parseDateParam(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid end_date: "+err.Error())
			return
		}
		filter.EndDate = &t
	}
	resp, err := h.svc.List(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list invocations")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseDateParam accepts RFC3339 timestamps as well as YYYY-MM-DD dates
// (interpreted as UTC midnight).
func parseDateParam(value string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("expected RFC3339 timestamp or YYYY-MM-DD date")
}

func apiMatchPatterns(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if pattern == "*" || pattern == value {
			return true
		}
	}
	return false
}

func apiMatchAgentIDPattern(pattern, agentID string) bool {
	return pattern == "" || pattern == "*" || pattern == agentID
}

// annotatedTool is the on-the-wire shape used for tools/list when atryum is
// able to compute a per-tool policy disposition. It mirrors mcp.Tool but adds
// an `annotations` block plus a description prefix so models that ignore
// unknown fields still see the policy hint inline.
type annotatedTool struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	InputSchema json.RawMessage    `json:"inputSchema,omitempty"`
	Annotations *atryumAnnotations `json:"annotations,omitempty"`
}

type atryumAnnotations struct {
	Atryum atryumToolPolicy `json:"atryum"`
}

type atryumToolPolicy struct {
	EffectiveAction string `json:"effective_action"`
	MatchedRuleID   string `json:"matched_rule_id,omitempty"`
}

// annotateToolsWithPolicy decorates each tool with its effective approval
// disposition for the current agent so the model sees the policy at the moment
// it picks a tool. Annotation requires both rulesRepo and a concrete server;
// in aggregate mode (server == "") we cannot reliably attribute tools to a
// server, so we return the tools unchanged.
func (h *Handler) annotateToolsWithPolicy(ctx context.Context, server string, tools []mcp.Tool) []any {
	out := make([]any, len(tools))
	if h.rulesRepo == nil || strings.TrimSpace(server) == "" {
		for i, t := range tools {
			out[i] = t
		}
		return out
	}
	rules, err := h.rulesRepo.List(ctx)
	if err != nil {
		for i, t := range tools {
			out[i] = t
		}
		return out
	}
	agentID := auth.AgentIDFromContext(ctx)
	agentCUID := h.resolveAgentRecordForRules(ctx, agentID)
	for i, t := range tools {
		if t.Name == agentRulesToolName {
			out[i] = t
			continue
		}
		action, matched := effectiveActionForTool(rules, server, t.Name, agentID, agentCUID)
		desc := t.Description
		if action != "" {
			prefix := "[atryum policy: " + action + "] "
			if desc == "" {
				desc = strings.TrimSpace(prefix)
			} else {
				desc = prefix + desc
			}
		}
		out[i] = annotatedTool{
			Name:        t.Name,
			Description: desc,
			InputSchema: t.InputSchema,
			Annotations: &atryumAnnotations{Atryum: atryumToolPolicy{
				EffectiveAction: action,
				MatchedRuleID:   matched,
			}},
		}
	}
	return out
}

// effectiveActionForTool returns the action of the first enabled rule that
// matches (server, tool, agentID, agentCUID), mirroring invocation.matchRules priority order.
// When no rule matches, it returns RuleActionHumanApproval (the default).
func effectiveActionForTool(rules []store.Rule, server, tool, agentID, agentCUID string) (string, string) {
	for _, r := range rules {
		if !apiRuleMatches(r, server, tool, agentID, agentCUID, true) {
			continue
		}
		return r.Action, r.ID
	}
	return invocation.RuleActionHumanApproval, ""
}

// appendRulesContextToToolResult adds an extra text content block to a denied
// tool call result describing the applicable rules and effective action, so
// the model can learn the policy through feedback instead of having to call
// atryum.rules.get explicitly.
func (h *Handler) appendRulesContextToToolResult(ctx context.Context, result any, server, tool string) any {
	if h.rulesRepo == nil {
		return result
	}
	rulesResp, err := h.buildAgentRulesResponse(ctx, auth.AgentIDFromContext(ctx), server, tool)
	if err != nil {
		return result
	}
	body, err := json.MarshalIndent(rulesResp, "", "  ")
	if err != nil {
		return result
	}
	m, ok := result.(map[string]any)
	if !ok {
		return result
	}
	rulesText := "Atryum approval rules applicable to this call:\n" + string(body)
	if existing, ok := m["content"].([]map[string]any); ok {
		m["content"] = append(existing, map[string]any{"type": "text", "text": rulesText})
		return m
	}
	if existing, ok := m["content"].([]any); ok {
		m["content"] = append(existing, map[string]any{"type": "text", "text": rulesText})
		return m
	}
	m["content"] = []map[string]any{{"type": "text", "text": rulesText}}
	return m
}

func appendAtryumTools(tools []mcp.Tool) []mcp.Tool {
	for _, tool := range tools {
		if tool.Name == agentRulesToolName {
			return tools
		}
	}
	return append(tools, mcp.Tool{
		Name:        agentRulesToolName,
		Description: "Return the approval rules and effective action for this agent. Call this before choosing tools so you can prefer auto_approve tools and avoid tools that require human_approval or auto_deny.",
		InputSchema: agentRulesToolInputSchema,
	})
}

func (h *Handler) callAgentRulesTool(ctx context.Context, currentServer string, args map[string]any) (map[string]any, error) {
	server := strings.TrimSpace(currentServer)
	if server == "" {
		server = stringArg(args, "server")
	}
	if server == "" {
		server = stringArg(args, "source")
	}
	tool := stringArg(args, "tool")
	resp, err := h.buildAgentRulesResponse(ctx, auth.AgentIDFromContext(ctx), server, tool)
	if err != nil {
		return nil, err
	}
	body, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"content": []map[string]any{{
			"type": "text",
			"text": string(body),
		}},
		"isError": false,
	}, nil
}

func stringArg(args map[string]any, name string) string {
	if args == nil {
		return ""
	}
	value, ok := args[name]
	if !ok {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func (h *Handler) adminInvocations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	filter := invocation.InvocationListFilter{
		Offset:     readUintQuery(r, "offset", 0),
		Limit:      readUintQuery(r, "limit", 50),
		Server:     strings.TrimSpace(r.URL.Query().Get("server")),
		Tool:       strings.TrimSpace(r.URL.Query().Get("tool")),
		Status:     strings.TrimSpace(r.URL.Query().Get("status")),
		ClientName: strings.TrimSpace(r.URL.Query().Get("client_name")),
	}
	resp, err := h.svc.List(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) adminInvocationStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	filter := invocation.InvocationListFilter{
		Offset:     0,
		Limit:      readUintQuery(r, "limit", 50),
		Server:     strings.TrimSpace(r.URL.Query().Get("server")),
		Tool:       strings.TrimSpace(r.URL.Query().Get("tool")),
		Status:     strings.TrimSpace(r.URL.Query().Get("status")),
		ClientName: strings.TrimSpace(r.URL.Query().Get("client_name")),
	}
	ctx := r.Context()
	lastSignature := ""
	for {
		resp, err := h.svc.List(ctx, filter)
		if err != nil {
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", mustJSONString(map[string]any{"message": err.Error()}))
			flusher.Flush()
			return
		}
		signature := invocationSignature(resp.Items)
		if signature != lastSignature {
			payload := invocationStreamEnvelope{Items: resp.Items}
			fmt.Fprintf(w, "event: invocations\ndata: %s\n\n", mustJSONString(payload))
			flusher.Flush()
			lastSignature = signature
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (h *Handler) adminInvocationDetail(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/invocations/")
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if strings.HasSuffix(trimmed, "/events") {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		id := strings.TrimSuffix(trimmed, "/events")
		id = strings.TrimSuffix(id, "/")
		filter := invocation.EventListFilter{Offset: readUintQuery(r, "offset", 0), Limit: readUintQuery(r, "limit", 200)}
		events, err := h.svc.Events(r.Context(), id, filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, events)
		return
	}
	if strings.HasSuffix(trimmed, "/approve") {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		id := strings.TrimSuffix(trimmed, "/approve")
		id = strings.TrimSuffix(id, "/")
		var req ApproveRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.CreateRule != nil {
			if err := validateRuleInput(*req.CreateRule); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			inv, err := h.svc.Get(r.Context(), id)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			anchorID := ""
			if inv.MatchedRuleID != nil {
				anchorID = *inv.MatchedRuleID
			}
			enabled := true
			if req.CreateRule.Enabled != nil {
				enabled = *req.CreateRule.Enabled
			}
			newRule := store.Rule{
				ID:              "rule_" + newUUID(),
				Action:          req.CreateRule.Action,
				ServerPatterns:  normalizePatternSlice(req.CreateRule.ServerPatterns),
				ToolPatterns:    normalizePatternSlice(req.CreateRule.ToolPatterns),
				AgentIDPattern:  defaultPattern(req.CreateRule.AgentIDPattern),
				ModelConfigCUID: req.CreateRule.ModelConfigCUID,
				AgentCUIDs:      normalizePatternSlice(req.CreateRule.AgentCUIDs),
				Description:     req.CreateRule.Description,
				Enabled:         enabled,
			}
			if err := h.rulesRepo.InsertBefore(r.Context(), anchorID, newRule); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if err := h.svc.Approve(r.Context(), id); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if strings.HasSuffix(trimmed, "/summarize") {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		id := strings.TrimSuffix(trimmed, "/summarize")
		id = strings.TrimSuffix(id, "/")
		h.summarizeInvocation(w, r, id)
		return
	}
	if strings.HasSuffix(trimmed, "/deny") {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		id := strings.TrimSuffix(trimmed, "/deny")
		id = strings.TrimSuffix(id, "/")
		var req DenyRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.CreateRule != nil {
			if err := validateRuleInput(*req.CreateRule); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			inv, err := h.svc.Get(r.Context(), id)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			anchorID := ""
			if inv.MatchedRuleID != nil {
				anchorID = *inv.MatchedRuleID
			}
			enabled := true
			if req.CreateRule.Enabled != nil {
				enabled = *req.CreateRule.Enabled
			}
			newRule := store.Rule{
				ID:              "rule_" + newUUID(),
				Action:          req.CreateRule.Action,
				ServerPatterns:  normalizePatternSlice(req.CreateRule.ServerPatterns),
				ToolPatterns:    normalizePatternSlice(req.CreateRule.ToolPatterns),
				AgentIDPattern:  defaultPattern(req.CreateRule.AgentIDPattern),
				ModelConfigCUID: req.CreateRule.ModelConfigCUID,
				AgentCUIDs:      normalizePatternSlice(req.CreateRule.AgentCUIDs),
				Description:     req.CreateRule.Description,
				Enabled:         enabled,
			}
			if err := h.rulesRepo.InsertBefore(r.Context(), anchorID, newRule); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if err := h.svc.Deny(r.Context(), id, req.Message); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	resp, err := h.svc.Get(r.Context(), trimmed)
	if err != nil {
		status := http.StatusInternalServerError
		if err == sql.ErrNoRows {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// SummarizeInvocationRequest is the JSON body accepted by
// POST /api/v1/admin/invocations/{id}/summarize.
type SummarizeInvocationRequest struct {
	ModelConfigCUID string `json:"model_config_cuid"`
}

// SummarizeInvocationResponse is the JSON shape returned by
// POST /api/v1/admin/invocations/{id}/summarize.
type SummarizeInvocationResponse struct {
	InvocationID string `json:"invocation_id"`
	Summary      string `json:"summary"`
}

// summarizeInvocation loads the invocation by id, forwards it to the VM
// backend's /summarize-invocation endpoint, and returns the LLM-generated
// summary.
func (h *Handler) summarizeInvocation(w http.ResponseWriter, r *http.Request, id string) {
	if id == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	h.debugf("summarizing invocation %s", id)
	if h.summarizeClient == nil {
		writeError(w, http.StatusServiceUnavailable, "backend not configured")
		return
	}

	// Body is optional; when present it may override the model config from
	// settings. Empty body or empty model_config_cuid means "use settings".
	var req SummarizeInvocationRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
	}
	modelConfigCUID := strings.TrimSpace(req.ModelConfigCUID)

	inv, err := h.svc.Get(r.Context(), id)
	if err != nil {
		status := http.StatusInternalServerError
		if err == sql.ErrNoRows {
			status = http.StatusNotFound
		}
		log.Printf("[summarizeInvocation] load invocation %s failed: %v", id, err)
		writeError(w, status, err.Error())
		return
	}

	// Round-trip via JSON so the backend sees the same shape as the admin
	// detail endpoint (input/result/error are json.RawMessage on the wire).
	raw, err := json.Marshal(inv)
	if err != nil {
		log.Printf("[summarizeInvocation] encode invocation %s failed: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to encode invocation: "+err.Error())
		return
	}
	var invMap map[string]any
	if err := json.Unmarshal(raw, &invMap); err != nil {
		log.Printf("[summarizeInvocation] normalize invocation %s failed: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to normalize invocation: "+err.Error())
		return
	}

	orgCUID := ""
	if h.agentSyncSettingsRepo != nil {
		if s, sErr := h.agentSyncSettingsRepo.Get(r.Context()); sErr == nil {
			orgCUID = s.OrgCUID
			if modelConfigCUID == "" {
				modelConfigCUID = strings.TrimSpace(s.SummaryModelConfigCUID)
			}
		} else {
			log.Printf("[summarizeInvocation] read agent_sync_settings failed: %v", sErr)
		}
	}

	if modelConfigCUID == "" {
		writeError(w, http.StatusBadRequest, "summary model configuration is not set; configure one on the Settings page")
		return
	}

	resp, err := h.summarizeClient.SummarizeInvocation(r.Context(), backendclient.SummarizeInvocationRequest{
		ModelConfigCUID: modelConfigCUID,
		OrgCUID:         orgCUID,
		Invocation:      invMap,
	})
	if err != nil {
		log.Printf("[summarizeInvocation] backend call failed for invocation %s (model_config_cuid=%s): %v", id, modelConfigCUID, err)
		writeError(w, http.StatusBadGateway, "failed to summarize invocation: "+err.Error())
		return
	}

	// Persist the generated summary on the invocation row so subsequent
	// Get/List calls expose it and the UI can render it without re-calling the LLM.
	updated, err := h.svc.SetSummary(r.Context(), inv.InvocationID, resp.Summary)
	if err != nil {
		log.Printf("[summarizeInvocation] persist summary for invocation %s failed: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to persist summary: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, SummarizeInvocationResponse{
		InvocationID: updated.InvocationID,
		Summary:      updated.Summary,
	})
}

func (h *Handler) adminServers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		filter := mcp.ServerFilter{Offset: readUintQuery(r, "offset", 0), Limit: readUintQuery(r, "limit", 50)}
		if enabledRaw := strings.TrimSpace(r.URL.Query().Get("enabled")); enabledRaw != "" {
			enabled, err := strconv.ParseBool(enabledRaw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid enabled filter")
				return
			}
			filter.Enabled = &enabled
		}
		resp, err := h.serverSvc.List(r.Context(), filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
	case http.MethodPost:
		var req AdminServerUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		server, err := h.serverSvc.Upsert(r.Context(), req.Name, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, server)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) adminServerDetail(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/servers/")
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if strings.HasSuffix(trimmed, "/tools") {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		name := strings.TrimSuffix(trimmed, "/tools")
		name = strings.TrimSuffix(name, "/")
		tools, err := h.svc.ListTools(r.Context(), name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if tools == nil {
			tools = []mcp.Tool{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": tools})
		return
	}
	if strings.HasSuffix(trimmed, "/test") {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		name := strings.TrimSuffix(trimmed, "/test")
		name = strings.TrimSuffix(name, "/")
		h.debugf("admin server test request method=%s path=%s server=%s remote=%s origin=%q referer=%q user_agent=%q content_length=%d", r.Method, r.URL.Path, name, r.RemoteAddr, r.Header.Get("Origin"), r.Header.Get("Referer"), r.UserAgent(), r.ContentLength)
		resp, err := h.serverSvc.Test(r.Context(), name)
		if err != nil {
			h.debugf("admin server test error server=%s err=%v", name, err)
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		h.debugf("admin server test response server=%s ok=%t connection_status=%s auth_status=%s reauth_needed=%t last_check_ok=%t message=%q action_required=%q", name, resp.Ok, resp.ConnectionStatus, resp.AuthStatus, resp.ReauthNeeded, resp.LastCheckOK, resp.Message, debugStringPtr(resp.ActionRequired))
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if strings.HasSuffix(trimmed, "/connect") {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		name := strings.TrimSuffix(trimmed, "/connect")
		name = strings.TrimSuffix(name, "/")
		resp, err := h.serverSvc.StartConnect(r.Context(), name, baseURL(r))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if strings.HasSuffix(trimmed, "/connect/status") {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		name := strings.TrimSuffix(trimmed, "/connect/status")
		name = strings.TrimSuffix(name, "/")
		resp, err := h.serverSvc.GetConnectStatus(r.Context(), name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	name := trimmed
	switch r.Method {
	case http.MethodGet:
		server, err := h.serverSvc.Get(r.Context(), name)
		if err != nil {
			status := http.StatusInternalServerError
			if err == sql.ErrNoRows {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, server)
	case http.MethodPut:
		var req AdminServerUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		server, err := h.serverSvc.Upsert(r.Context(), name, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, server)
	case http.MethodDelete:
		disable := strings.EqualFold(r.URL.Query().Get("mode"), "disable") || strings.EqualFold(r.URL.Query().Get("disable"), "true")
		if err := h.serverSvc.Delete(r.Context(), name, disable); err != nil {
			status := http.StatusInternalServerError
			if err == sql.ErrNoRows {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "disabled": disable})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) oauthCallback(w http.ResponseWriter, r *http.Request) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	errText := strings.TrimSpace(r.URL.Query().Get("error"))
	resp, err := h.serverSvc.CompleteConnect(r.Context(), state, code, errText)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<html><body><h1>OAuth connect failed</h1><p>` + escapeHTMLString(err.Error()) + `</p><script>window.close && window.close()</script></body></html>`))
		return
	}
	message := "OAuth connect completed. You can return to Atryum."
	if resp.Message != nil {
		message = *resp.Message
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<html><body><h1>OAuth connect complete</h1><p>` + escapeHTMLString(message) + `</p><script>window.close && window.close()</script></body></html>`))
}

func (h *Handler) adminPolicy(w http.ResponseWriter, r *http.Request) {
	if h.policyRegistry == nil {
		writeError(w, http.StatusServiceUnavailable, "policy registry not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		active := h.policyRegistry.Active()
		if active == nil {
			writeError(w, http.StatusInternalServerError, "no active policy provider")
			return
		}
		writeJSON(w, http.StatusOK, h.buildPolicyStatus(active))

	case http.MethodPut:
		var req PolicyUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if strings.TrimSpace(req.Provider) == "" {
			writeError(w, http.StatusBadRequest, "provider is required")
			return
		}
		if err := h.policyRegistry.SetActive(req.Provider); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, h.buildPolicyStatus(h.policyRegistry.Active()))

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) buildPolicyStatus(active policy.Provider) PolicyStatusResponse {
	resp := PolicyStatusResponse{
		ActiveProvider: active.ID(),
		DisplayName:    active.DisplayName(),
	}
	for _, p := range h.policyRegistry.Providers() {
		resp.Providers = append(resp.Providers, PolicyProviderInfo{ID: p.ID(), DisplayName: p.DisplayName()})
	}
	return resp
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": message}})
}

func debugStringPtr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (h *Handler) adminRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rules, err := h.rulesRepo.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		items := make([]AdminRule, 0, len(rules))
		for _, rule := range rules {
			items = append(items, toAdminRule(rule))
		}
		writeJSON(w, http.StatusOK, RuleListResponse{Items: items})
	case http.MethodPost:
		var req AdminRuleInput
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if err := validateRuleInput(req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		order, err := h.rulesRepo.NextOrder(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		rule := store.Rule{
			ID:              "rule_" + newUUID(),
			Action:          req.Action,
			ServerPatterns:  normalizePatternSlice(req.ServerPatterns),
			ToolPatterns:    normalizePatternSlice(req.ToolPatterns),
			AgentIDPattern:  defaultPattern(req.AgentIDPattern),
			ModelConfigCUID: req.ModelConfigCUID,
			AgentCUIDs:      normalizePatternSlice(req.AgentCUIDs),
			Description:     req.Description,
			Enabled:         enabled,
			Order:           order,
		}
		if err := h.rulesRepo.Create(r.Context(), rule); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		created, err := h.rulesRepo.Get(r.Context(), rule.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, toAdminRule(created))
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) adminRuleDetail(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/rules/")
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	if strings.HasSuffix(trimmed, "/move") {
		if r.Method != http.MethodPut {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		id := strings.TrimSuffix(trimmed, "/move")
		id = strings.TrimSuffix(id, "/")
		var body struct {
			Direction string `json:"direction"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		rules, err := h.rulesRepo.Move(r.Context(), id, body.Direction)
		if err != nil {
			status := http.StatusBadRequest
			if err == sql.ErrNoRows {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error())
			return
		}
		items := make([]AdminRule, 0, len(rules))
		for _, rule := range rules {
			items = append(items, toAdminRule(rule))
		}
		writeJSON(w, http.StatusOK, RuleListResponse{Items: items})
		return
	}

	id := trimmed
	switch r.Method {
	case http.MethodGet:
		rule, err := h.rulesRepo.Get(r.Context(), id)
		if err != nil {
			status := http.StatusInternalServerError
			if err == sql.ErrNoRows {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, toAdminRule(rule))
	case http.MethodPut:
		var req AdminRuleInput
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if err := validateRuleInput(req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		existing, err := h.rulesRepo.Get(r.Context(), id)
		if err != nil {
			status := http.StatusInternalServerError
			if err == sql.ErrNoRows {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error())
			return
		}
		enabled := existing.Enabled
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		existing.Action = req.Action
		existing.ServerPatterns = normalizePatternSlice(req.ServerPatterns)
		existing.ToolPatterns = normalizePatternSlice(req.ToolPatterns)
		existing.AgentIDPattern = defaultPattern(req.AgentIDPattern)
		existing.ModelConfigCUID = req.ModelConfigCUID
		existing.AgentCUIDs = normalizePatternSlice(req.AgentCUIDs)
		existing.Description = req.Description
		existing.Enabled = enabled
		if err := h.rulesRepo.Update(r.Context(), existing); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		updated, err := h.rulesRepo.Get(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, toAdminRule(updated))
	case http.MethodDelete:
		if err := h.rulesRepo.Delete(r.Context(), id); err != nil {
			status := http.StatusInternalServerError
			if err == sql.ErrNoRows {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ─── Model config handler ─────────────────────────────────────────────────────

func (h *Handler) adminModelConfigs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.backendClient == nil {
		writeError(w, http.StatusServiceUnavailable, "backend not configured")
		return
	}
	resp, err := h.backendClient.FetchModelConfigs(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to fetch model configs: "+err.Error())
		return
	}
	items := make([]AdminModelConfig, 0, len(resp.Items))
	for _, c := range resp.Items {
		items = append(items, AdminModelConfig{CUID: c.CUID, Name: c.Name})
	}
	writeJSON(w, http.StatusOK, ModelConfigListResponse{Items: items, Total: len(items)})
}

// ─── Agent Sync Settings handlers ────────────────────────────────────────────

// AgentSyncSettingsResponse is the JSON shape returned by GET /admin/settings
// and PUT /admin/settings. SyncError is non-empty when the post-save agent
// sync failed; settings are persisted regardless.
type AgentSyncSettingsResponse struct {
	OrgCUID                string `json:"org_cuid"`
	AgentRecordTypeSlug    string `json:"agent_record_type_slug"`
	ConstitutionFieldKey   string `json:"constitution_field_key"`
	SummaryModelConfigCUID string `json:"summary_model_config_cuid"`
	DefaultAgentVMCUID     string `json:"default_agent_vm_cuid"`
	UpdatedAt              string `json:"updated_at,omitempty"`
	SyncError              string `json:"sync_error,omitempty"`
}

// AgentSyncSettingsInput is the JSON body accepted by PUT /admin/settings.
type AgentSyncSettingsInput struct {
	OrgCUID                string `json:"org_cuid"`
	AgentRecordTypeSlug    string `json:"agent_record_type_slug"`
	ConstitutionFieldKey   string `json:"constitution_field_key"`
	SummaryModelConfigCUID string `json:"summary_model_config_cuid"`
	DefaultAgentVMCUID     string `json:"default_agent_vm_cuid"`
}

func (h *Handler) adminSettings(w http.ResponseWriter, r *http.Request) {
	if h.agentSyncSettingsRepo == nil {
		writeError(w, http.StatusServiceUnavailable, "settings not available")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s, err := h.agentSyncSettingsRepo.Get(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read settings: "+err.Error())
			return
		}
		resp := AgentSyncSettingsResponse{
			OrgCUID:                s.OrgCUID,
			AgentRecordTypeSlug:    s.AgentRecordTypeSlug,
			ConstitutionFieldKey:   s.ConstitutionFieldKey,
			SummaryModelConfigCUID: s.SummaryModelConfigCUID,
			DefaultAgentVMCUID:     s.DefaultAgentVMCUID,
		}
		if !s.UpdatedAt.IsZero() {
			resp.UpdatedAt = s.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		writeJSON(w, http.StatusOK, resp)
	case http.MethodPut:
		var input AgentSyncSettingsInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		// Read current settings to detect whether the org or record type changed.
		current, err := h.agentSyncSettingsRepo.Get(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read current settings: "+err.Error())
			return
		}
		orgChanged := current.OrgCUID != "" && current.OrgCUID != input.OrgCUID
		slugChanged := current.AgentRecordTypeSlug != "" && current.AgentRecordTypeSlug != input.AgentRecordTypeSlug
		if (orgChanged || slugChanged) && h.agentsRepo != nil {
			if delErr := h.agentsRepo.DeleteAll(r.Context()); delErr != nil {
				writeError(w, http.StatusInternalServerError, "failed to delete agents: "+delErr.Error())
				return
			}
		}
		if err := h.agentSyncSettingsRepo.Save(r.Context(), store.AgentSyncSettings{
			OrgCUID:                input.OrgCUID,
			AgentRecordTypeSlug:    input.AgentRecordTypeSlug,
			ConstitutionFieldKey:   input.ConstitutionFieldKey,
			SummaryModelConfigCUID: input.SummaryModelConfigCUID,
			DefaultAgentVMCUID:     input.DefaultAgentVMCUID,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save settings: "+err.Error())
			return
		}
		// Trigger an agent sync whenever the new settings include a complete
		// org + record-type pair, so the agent list is immediately up-to-date.
		syncErr := ""
		if h.syncAgentsFn != nil && input.OrgCUID != "" && input.AgentRecordTypeSlug != "" {
			if err := h.syncAgentsFn(r.Context()); err != nil {
				// Non-fatal: settings are already saved; surface the error to the
				// caller so the UI can show a warning rather than blocking the save.
				syncErr = err.Error()
			}
		}
		s, err := h.agentSyncSettingsRepo.Get(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read saved settings: "+err.Error())
			return
		}
		log.Printf("[adminSettings] GET after save: org_cuid=%q record_type=%q", s.OrgCUID, s.AgentRecordTypeSlug)
		resp := AgentSyncSettingsResponse{
			OrgCUID:                s.OrgCUID,
			AgentRecordTypeSlug:    s.AgentRecordTypeSlug,
			ConstitutionFieldKey:   s.ConstitutionFieldKey,
			SummaryModelConfigCUID: s.SummaryModelConfigCUID,
			DefaultAgentVMCUID:     s.DefaultAgentVMCUID,
			SyncError:              syncErr,
		}
		if !s.UpdatedAt.IsZero() {
			resp.UpdatedAt = s.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		writeJSON(w, http.StatusOK, resp)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ─── VM Discovery proxy handlers ─────────────────────────────────────────────

type VMOrgItem struct {
	CUID string `json:"cuid"`
	Name string `json:"name"`
}

type VMOrgListResponse struct {
	Items     []VMOrgItem `json:"items"`
	Total     int         `json:"total"`
	AuthMode  string      `json:"auth_mode,omitempty"`
	SingleOrg bool        `json:"single_org,omitempty"`
}

type VMRecordTypeItem struct {
	CUID string `json:"cuid"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type VMRecordTypeListResponse struct {
	Items []VMRecordTypeItem `json:"items"`
	Total int                `json:"total"`
}

type VMCustomFieldItem struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	FieldType string `json:"field_type"`
}

type VMCustomFieldListResponse struct {
	Items []VMCustomFieldItem `json:"items"`
	Total int                 `json:"total"`
}

func (h *Handler) adminVMOrganizations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.backendClient == nil {
		writeError(w, http.StatusServiceUnavailable, "backend not configured")
		return
	}
	resp, err := h.backendClient.FetchOrganizations(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to fetch organizations: "+err.Error())
		return
	}
	items := make([]VMOrgItem, 0, len(resp.Items))
	for _, o := range resp.Items {
		items = append(items, VMOrgItem{CUID: o.CUID, Name: o.Name})
	}
	writeJSON(w, http.StatusOK, VMOrgListResponse{Items: items, Total: len(items), AuthMode: resp.AuthMode, SingleOrg: resp.SingleOrg})
}

func (h *Handler) adminVMRecordTypes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.backendClient == nil {
		writeError(w, http.StatusServiceUnavailable, "backend not configured")
		return
	}
	orgCUID := r.URL.Query().Get("org_cuid")
	if orgCUID == "" {
		writeError(w, http.StatusBadRequest, "org_cuid query parameter is required")
		return
	}
	resp, err := h.backendClient.FetchPrimaryRecordTypes(r.Context(), orgCUID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to fetch record types: "+err.Error())
		return
	}
	items := make([]VMRecordTypeItem, 0, len(resp.Items))
	for _, rt := range resp.Items {
		items = append(items, VMRecordTypeItem{CUID: rt.CUID, Slug: rt.Slug, Name: rt.Name})
	}
	writeJSON(w, http.StatusOK, VMRecordTypeListResponse{Items: items, Total: len(items)})
}

func (h *Handler) adminVMCustomFields(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.backendClient == nil {
		writeError(w, http.StatusServiceUnavailable, "backend not configured")
		return
	}
	orgCUID := r.URL.Query().Get("org_cuid")
	if orgCUID == "" {
		writeError(w, http.StatusBadRequest, "org_cuid query parameter is required")
		return
	}
	slug := r.URL.Query().Get("primary_record_type_slug")
	resp, err := h.backendClient.FetchCustomFields(r.Context(), orgCUID, slug)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to fetch custom fields: "+err.Error())
		return
	}
	items := make([]VMCustomFieldItem, 0, len(resp.Items))
	for _, cf := range resp.Items {
		items = append(items, VMCustomFieldItem{Key: cf.Key, Name: cf.Name, FieldType: cf.FieldType})
	}
	writeJSON(w, http.StatusOK, VMCustomFieldListResponse{Items: items, Total: len(items)})
}

// ─────────────────────────────────────────────────────────────────────────────

func toAdminRule(r store.Rule) AdminRule {
	sp := r.ServerPatterns
	if sp == nil {
		sp = []string{}
	}
	tp := r.ToolPatterns
	if tp == nil {
		tp = []string{}
	}
	ac := r.AgentCUIDs
	if ac == nil {
		ac = []string{}
	}
	return AdminRule{
		ID:              r.ID,
		Action:          r.Action,
		ServerPatterns:  sp,
		ToolPatterns:    tp,
		AgentIDPattern:  r.AgentIDPattern,
		ModelConfigCUID: r.ModelConfigCUID,
		AgentCUIDs:      ac,
		Description:     r.Description,
		Enabled:         r.Enabled,
		Order:           r.Order,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
}

func normalizePatternSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ─── Agent handlers ───────────────────────────────────────────────────────────

func (h *Handler) adminAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	records, err := h.agentsRepo.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]AdminAgent, 0, len(records))
	for _, a := range records {
		items = append(items, toAdminAgent(a))
	}
	writeJSON(w, http.StatusOK, AgentListResponse{Items: items})
}

func (h *Handler) adminAgentDetail(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/agents/")
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	// POST /api/v1/admin/agents/sync — trigger a backend sync
	if trimmed == "sync" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if h.syncAgentsFn == nil {
			writeError(w, http.StatusServiceUnavailable, "agent sync not configured")
			return
		}
		if err := h.syncAgentsFn(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		records, err := h.agentsRepo.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		items := make([]AdminAgent, 0, len(records))
		for _, a := range records {
			items = append(items, toAdminAgent(a))
		}
		writeJSON(w, http.StatusOK, AgentListResponse{Items: items})
		return
	}

	// PATCH /api/v1/admin/agents/:id — update enabled
	id := trimmed
	switch r.Method {
	case http.MethodGet:
		record, err := h.agentsRepo.Get(r.Context(), id)
		if err != nil {
			status := http.StatusInternalServerError
			if err == sql.ErrNoRows {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, toAdminAgent(record))
	case http.MethodPatch:
		var req AdminAgentInput
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if err := h.agentsRepo.UpdateEnabled(r.Context(), id, req.Enabled); err != nil {
			status := http.StatusInternalServerError
			if err == sql.ErrNoRows {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error())
			return
		}
		if req.AgentIDs != nil {
			idsJSON, err := json.Marshal(req.AgentIDs)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to encode agent_ids")
				return
			}
			if err := h.agentsRepo.UpdateAgentIDs(r.Context(), id, string(idsJSON)); err != nil {
				status := http.StatusInternalServerError
				if err == sql.ErrNoRows {
					status = http.StatusNotFound
				}
				writeError(w, status, err.Error())
				return
			}
		}
		record, err := h.agentsRepo.Get(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, toAdminAgent(record))
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ─────────────────────────────────────────────────────────────────────────────

func validateRuleInput(req AdminRuleInput) error {
	switch req.Action {
	case "auto_approve", "auto_deny", "human_approval":
	case "ai_evaluation":
		if strings.TrimSpace(req.ModelConfigCUID) == "" {
			return fmt.Errorf("model_config_cuid is required for ai_evaluation rules")
		}
	default:
		return fmt.Errorf("action must be one of: auto_approve, auto_deny, human_approval, ai_evaluation")
	}
	return nil
}

func defaultPattern(p string) string {
	if strings.TrimSpace(p) == "" {
		return "*"
	}
	return p
}

func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("uuid generation failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func readUintQuery(r *http.Request, key string, fallback uint64) uint64 {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

type ServerAdminService struct {
	repo          serverRepo
	oauthRepo     *store.OAuthRepo
	client        *mcp.Client
	timeout       time.Duration
	publicBaseURL string
}

type serverRepo interface {
	ListServers(ctx context.Context, filter mcp.ServerFilter) ([]mcp.Upstream, int, error)
	GetServerAny(ctx context.Context, name string) (mcp.Upstream, error)
	UpsertServer(ctx context.Context, upstream mcp.Upstream) error
	UpdateServerStatus(ctx context.Context, name string, status mcp.ServerStatus) error
	DeleteServer(ctx context.Context, name string) error
	DisableServer(ctx context.Context, name string) error
}

func NewServerAdminService(repo serverRepo, oauthRepo *store.OAuthRepo, client *mcp.Client, timeout time.Duration, publicBaseURL string) *ServerAdminService {
	return &ServerAdminService{repo: repo, oauthRepo: oauthRepo, client: client, timeout: timeout, publicBaseURL: strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")}
}

func (s *ServerAdminService) List(ctx context.Context, filter mcp.ServerFilter) (ServerListResponse, error) {
	items, total, err := s.repo.ListServers(ctx, filter)
	if err != nil {
		return ServerListResponse{}, err
	}
	servers := make([]AdminServer, 0, len(items))
	for _, item := range items {
		servers = append(servers, s.adminViewWithGrantedScopes(ctx, item))
	}
	return ServerListResponse{Items: servers, Total: total, Offset: filter.Offset, Limit: normalizeLimit(filter.Limit, 50)}, nil
}

func (s *ServerAdminService) Get(ctx context.Context, name string) (AdminServer, error) {
	upstream, err := s.repo.GetServerAny(ctx, name)
	if err != nil {
		return AdminServer{}, err
	}
	return s.adminViewWithGrantedScopes(ctx, upstream), nil
}

// adminViewWithGrantedScopes is toAdminServer + an overlay of the actual
// scope string the AS granted on the latest successful token exchange.
// Pulled separately from oauth_credentials.scope so the UI can show both
// what we requested and what's actually live.
func (s *ServerAdminService) adminViewWithGrantedScopes(ctx context.Context, upstream mcp.Upstream) AdminServer {
	view := toAdminServer(upstream)
	if cred, err := s.oauthRepo.GetCredential(ctx, upstream.Name); err == nil {
		view.OAuthGrantedScopes = cred.Scope
	}
	return view
}

func (s *ServerAdminService) Upsert(ctx context.Context, name string, req AdminServerUpsertRequest) (AdminServer, error) {
	serverName := strings.TrimSpace(name)
	requestedName := strings.TrimSpace(req.Name)
	if serverName == "" {
		serverName = requestedName
	}
	if serverName != "" && requestedName != "" && requestedName != serverName {
		return AdminServer{}, fmt.Errorf("renaming servers is not supported; create a new server instead")
	}
	if serverName == "" {
		return AdminServer{}, fmt.Errorf("name is required")
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		return AdminServer{}, fmt.Errorf("mode is required")
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	// Load any existing row so we can preserve OAuth fields the form
	// chose not to re-send (notably client_secret, which is never echoed
	// back to the browser).
	existing, _ := s.repo.GetServerAny(ctx, serverName)

	upstream := mcp.Upstream{
		Name:        serverName,
		Mode:        mcp.UpstreamMode(mode),
		BaseURL:     strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"),
		AuthToken:   req.AuthToken,
		AuthHeaders: append([]mcp.AuthHeader(nil), req.AuthHeaders...),
		Timeout:     time.Duration(defaultIfZero(req.TimeoutSeconds, 30)) * time.Second,
		Command:     strings.TrimSpace(req.Command),
		Args:        append([]string(nil), req.Args...),
		Env:         cloneEnv(req.Env),
		Enabled:     enabled,
	}

	// OAuth fields from the request body win, except client_secret which
	// uses an empty-means-unchanged semantic (the form never receives the
	// stored secret, so submitting empty must not wipe it). Provenance is
	// flipped to "preshared" only when the admin actually changes the
	// client_id — a re-saved DCR row keeps its "dynamic" tag.
	upstream.OAuthClientID = strings.TrimSpace(req.OAuthClientID)
	upstream.OAuthAuthorizeURL = strings.TrimSpace(req.OAuthAuthorizeURL)
	upstream.OAuthTokenURL = strings.TrimSpace(req.OAuthTokenURL)
	upstream.OAuthScopes = strings.TrimSpace(req.OAuthScopes)
	upstream.OAuthClientRegistration = existing.OAuthClientRegistration
	if strings.TrimSpace(req.OAuthClientSecret) != "" {
		upstream.OAuthClientSecret = req.OAuthClientSecret
	} else {
		upstream.OAuthClientSecret = existing.OAuthClientSecret
	}
	if upstream.OAuthClientID != "" && upstream.OAuthClientID != existing.OAuthClientID {
		upstream.OAuthClientRegistration = mcp.ClientRegistrationPreshared
	}
	if upstream.OAuthClientID == "" {
		// Cleared — drop provenance so a re-discovery on next save can
		// pick the right registration mode again.
		upstream.OAuthClientRegistration = mcp.ClientRegistrationUnknown
	}

	prepared, prepareErr := authprovider.Prepare(ctx, authprovider.NewRegistry(), upstream)
	if prepareErr == nil {
		upstream = prepared
	}
	upstream.Status = inferServerStatus(upstream)
	if err := validateUpstream(upstream); err != nil {
		return AdminServer{}, err
	}
	if err := s.repo.UpsertServer(ctx, upstream); err != nil {
		return AdminServer{}, err
	}
	return s.adminViewWithGrantedScopes(ctx, upstream), nil
}

func (s *ServerAdminService) Delete(ctx context.Context, name string, disable bool) error {
	if disable {
		return s.repo.DisableServer(ctx, name)
	}
	return s.repo.DeleteServer(ctx, name)
}

func (s *ServerAdminService) Test(ctx context.Context, name string) (ServerTestResponse, error) {
	upstream, err := s.repo.GetServerAny(ctx, name)
	if err != nil {
		return ServerTestResponse{}, err
	}
	if token, tokenErr := s.oauthRepo.GetCredential(ctx, name); tokenErr == nil {
		upstream.AuthToken = token.AccessToken
	}
	testCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	result := s.client.TestConnection(testCtx, upstream)
	now := time.Now().UTC()
	upstream.Status.AuthType = inferServerStatus(upstream).AuthType
	upstream.Status.ConnectionStatus = result.ConnectionStatus
	upstream.Status.AuthStatus = result.AuthStatus
	upstream.Status.ReauthNeeded = result.ReauthNeeded
	upstream.Status.LastCheckedAt = &now
	upstream.Status.LastCheckOK = result.LastCheckOK
	upstream.Status.LastErrorSummary = result.LastErrorSummary
	upstream.Status.ActionRequired = result.ActionRequired
	if err := s.repo.UpdateServerStatus(ctx, name, upstream.Status); err != nil {
		return ServerTestResponse{}, err
	}
	return ServerTestResponse{Ok: result.Ok, Message: result.Message, ConnectionStatus: string(result.ConnectionStatus), AuthStatus: string(result.AuthStatus), ReauthNeeded: result.ReauthNeeded, LastCheckedAt: upstream.Status.LastCheckedAt, LastCheckOK: result.LastCheckOK, LastErrorSummary: result.LastErrorSummary, ActionRequired: result.ActionRequired}, nil
}

func (s *ServerAdminService) StartConnect(ctx context.Context, name string, appBaseURL string) (OAuthConnectStartResponse, error) {
	upstream, err := s.repo.GetServerAny(ctx, name)
	if err != nil {
		return OAuthConnectStartResponse{}, err
	}
	registry := authprovider.NewRegistry()
	provider, err := registry.Get(upstream.OAuthProviderID)
	if err != nil {
		provider, err = registry.Detect(ctx, upstream)
		if err != nil {
			return OAuthConnectStartResponse{}, fmt.Errorf("no connectable auth provider detected for this server")
		}
	}
	stateToken, err := randomToken(24)
	if err != nil {
		return OAuthConnectStartResponse{}, err
	}
	redirectBaseURL := strings.TrimRight(appBaseURL, "/")
	if s.publicBaseURL != "" {
		redirectBaseURL = s.publicBaseURL
	}
	redirectURI := redirectBaseURL + "/api/v1/admin/oauth/callback"
	connectReq, err := provider.BuildConnectRequest(ctx, upstream, redirectURI, stateToken)
	if err != nil {
		return OAuthConnectStartResponse{}, err
	}
	if connectReq.UpstreamPatch != nil {
		connectReq.UpstreamPatch(&upstream)
		if err := s.repo.UpsertServer(ctx, upstream); err != nil {
			return OAuthConnectStartResponse{}, err
		}
	}
	if err := s.oauthRepo.UpsertConnectSession(ctx, store.OAuthConnectSession{State: stateToken, ServerName: name, Status: "pending", CodeVerifier: connectReq.CodeVerifier, RedirectURI: redirectURI, StartedAt: time.Now().UTC()}); err != nil {
		return OAuthConnectStartResponse{}, err
	}
	log.Printf("[mcp-auth] start_connect server=%s state=%s verifier_len=%d redirect_uri=%s", name, stateToken, len(connectReq.CodeVerifier), redirectURI)
	return OAuthConnectStartResponse{ConnectURL: connectReq.URL, State: stateToken}, nil
}

func (s *ServerAdminService) GetConnectStatus(ctx context.Context, name string) (OAuthConnectStatusResponse, error) {
	session, err := s.oauthRepo.GetLatestConnectSessionByServer(ctx, name)
	if err != nil {
		if err == sql.ErrNoRows {
			return OAuthConnectStatusResponse{Status: "unknown"}, nil
		}
		return OAuthConnectStatusResponse{}, err
	}
	return OAuthConnectStatusResponse{Status: session.Status, Message: session.ErrorMessage, StartedAt: &session.StartedAt, CompletedAt: session.CompletedAt}, nil
}

func (s *ServerAdminService) CompleteConnect(ctx context.Context, state string, code string, errorText string) (OAuthConnectStatusResponse, error) {
	if strings.TrimSpace(state) == "" {
		return OAuthConnectStatusResponse{}, fmt.Errorf("missing oauth state")
	}
	session, err := s.oauthRepo.GetConnectSession(ctx, state)
	if err != nil {
		return OAuthConnectStatusResponse{}, err
	}
	if errorText != "" {
		now := time.Now().UTC()
		_ = s.oauthRepo.UpsertConnectSession(ctx, store.OAuthConnectSession{State: session.State, ServerName: session.ServerName, Status: "failed", RedirectURI: session.RedirectURI, StartedAt: session.StartedAt, CompletedAt: &now, ErrorMessage: &errorText})
		upstream, getErr := s.repo.GetServerAny(ctx, session.ServerName)
		if getErr == nil {
			upstream.Status.AuthStatus = mcp.AuthStatusReauthNeeded
			upstream.Status.ReauthNeeded = true
			upstream.Status.ConnectionStatus = mcp.ConnectionStatusNeedsAttention
			upstream.Status.LastErrorSummary = &errorText
			action := "retry connect"
			upstream.Status.ActionRequired = &action
			_ = s.repo.UpdateServerStatus(ctx, session.ServerName, upstream.Status)
		}
		return OAuthConnectStatusResponse{Status: "failed", Message: &errorText, StartedAt: &session.StartedAt, CompletedAt: &now}, nil
	}
	if strings.TrimSpace(code) == "" {
		return OAuthConnectStatusResponse{}, fmt.Errorf("missing oauth code")
	}
	upstream, err := s.repo.GetServerAny(ctx, session.ServerName)
	if err != nil {
		return OAuthConnectStatusResponse{}, err
	}
	registry := authprovider.NewRegistry()
	provider, providerErr := registry.Get(upstream.OAuthProviderID)
	if providerErr != nil {
		provider, providerErr = registry.Detect(ctx, upstream)
	}
	if providerErr != nil {
		return OAuthConnectStatusResponse{}, providerErr
	}
	token, err := provider.ExchangeAuthCode(ctx, s.client, upstream, code, session.RedirectURI, authprovider.ConnectSession{State: session.State, CodeVerifier: session.CodeVerifier})
	if err != nil {
		now := time.Now().UTC()
		message := err.Error()
		_ = s.oauthRepo.UpsertConnectSession(ctx, store.OAuthConnectSession{State: session.State, ServerName: session.ServerName, Status: "failed", RedirectURI: session.RedirectURI, StartedAt: session.StartedAt, CompletedAt: &now, ErrorMessage: &message})
		upstream.Status.AuthStatus = mcp.AuthStatusInvalid
		upstream.Status.ReauthNeeded = true
		upstream.Status.ConnectionStatus = mcp.ConnectionStatusNeedsAttention
		upstream.Status.LastErrorSummary = &message
		action := "retry connect"
		upstream.Status.ActionRequired = &action
		_ = s.repo.UpdateServerStatus(ctx, session.ServerName, upstream.Status)
		return OAuthConnectStatusResponse{Status: "failed", Message: &message, StartedAt: &session.StartedAt, CompletedAt: &now}, nil
	}
	if err := s.oauthRepo.UpsertCredential(ctx, store.OAuthCredential{ServerName: session.ServerName, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, TokenType: token.TokenType, Scope: token.Scope, ExpiresAt: token.ExpiresAt}); err != nil {
		return OAuthConnectStatusResponse{}, err
	}
	upstream.AuthToken = token.AccessToken
	upstream.Status.AuthType = mcp.AuthTypeHosted
	upstream.Status.AuthStatus = mcp.AuthStatusReady
	upstream.Status.ReauthNeeded = false
	upstream.Status.ConnectionStatus = mcp.ConnectionStatusReady
	upstream.Status.LastErrorSummary = nil
	upstream.Status.ActionRequired = nil
	now := time.Now().UTC()
	upstream.Status.LastCheckedAt = &now
	upstream.Status.LastCheckOK = true
	if err := s.repo.UpdateServerStatus(ctx, session.ServerName, upstream.Status); err != nil {
		return OAuthConnectStatusResponse{}, err
	}
	message := "connected successfully"
	if err := s.oauthRepo.UpsertConnectSession(ctx, store.OAuthConnectSession{State: session.State, ServerName: session.ServerName, Status: "succeeded", RedirectURI: session.RedirectURI, StartedAt: session.StartedAt, CompletedAt: &now, ErrorMessage: nil}); err != nil {
		return OAuthConnectStatusResponse{}, err
	}
	return OAuthConnectStatusResponse{Status: "succeeded", Message: &message, StartedAt: &session.StartedAt, CompletedAt: &now}, nil
}

func (h *Handler) externalInvocations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req invocation.ExternalSubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.RequestID == nil {
		if rid := strings.TrimSpace(r.Header.Get("X-Request-Id")); rid != "" {
			req.RequestID = &rid
		}
	}
	if req.IdempotencyKey == nil {
		if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key != "" {
			req.IdempotencyKey = &key
		}
	}
	// Header fallbacks for callers that can't (or don't) put their
	// harness identity in the JSON body. Body fields win.
	if req.ClientName == "" {
		if v := strings.TrimSpace(r.Header.Get("X-Atryum-Client-Name")); v != "" {
			req.ClientName = v
		}
	}
	if req.ClientVersion == "" {
		if v := strings.TrimSpace(r.Header.Get("X-Atryum-Client-Version")); v != "" {
			req.ClientVersion = v
		}
	}
	// TODO(sc-16270): the User-Agent header is an additional fallback signal
	// for identifying raw-script callers (curl, python-requests, etc.). We're
	// leaving it as a debug log for now while we figure out what shapes
	// actually show up in the wild. If patterns emerge, plumb through a
	// real user_agent column and capture here.
	h.debugf("external submit user-agent=%q", r.Header.Get("User-Agent"))

	resp, err := h.svc.Submit(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) externalInvocationDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/external/invocations/"), "/")
	if id == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		resp, err := h.svc.Get(r.Context(), id)
		if err != nil {
			status := http.StatusInternalServerError
			if err == sql.ErrNoRows {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
	case http.MethodPatch:
		var update invocation.ExternalExecutionUpdate
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		resp, err := h.svc.RecordExecution(r.Context(), id, update)
		if err != nil {
			status := http.StatusBadRequest
			if err == sql.ErrNoRows {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func toAdminServer(upstream mcp.Upstream) AdminServer {
	return AdminServer{Name: upstream.Name, Mode: string(upstream.Mode), BaseURL: upstream.BaseURL, AuthToken: upstream.AuthToken, AuthHeaders: append([]mcp.AuthHeader(nil), upstream.AuthHeaders...), TimeoutSeconds: int(upstream.Timeout / time.Second), Command: upstream.Command, Args: append([]string(nil), upstream.Args...), Env: cloneEnv(upstream.Env), Enabled: upstream.Enabled, AuthType: string(upstream.Status.AuthType), ConnectionStatus: string(upstream.Status.ConnectionStatus), AuthStatus: string(upstream.Status.AuthStatus), ReauthNeeded: upstream.Status.ReauthNeeded, LastCheckedAt: upstream.Status.LastCheckedAt, LastCheckOK: upstream.Status.LastCheckOK, LastErrorSummary: upstream.Status.LastErrorSummary, ActionRequired: upstream.Status.ActionRequired, OAuthProviderID: upstream.OAuthProviderID, OAuthProviderLabel: upstream.OAuthProviderLabel, OAuthClientRegistration: string(upstream.OAuthClientRegistration), OAuthClientID: upstream.OAuthClientID, OAuthAuthorizeURL: upstream.OAuthAuthorizeURL, OAuthTokenURL: upstream.OAuthTokenURL, OAuthScopes: upstream.OAuthScopes, HasOAuthClientSecret: strings.TrimSpace(upstream.OAuthClientSecret) != ""}
}

func validateUpstream(upstream mcp.Upstream) error {
	switch upstream.Mode {
	case mcp.UpstreamModeHTTP:
		if upstream.BaseURL == "" {
			return fmt.Errorf("base_url is required for http mode")
		}
	case mcp.UpstreamModeStdio:
		if upstream.Command == "" {
			return fmt.Errorf("command is required for stdio mode")
		}
	default:
		return fmt.Errorf("unsupported mode %q", upstream.Mode)
	}
	return nil
}

func defaultIfZero(value int, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

func normalizeLimit(limit uint64, fallback uint64) uint64 {
	if limit == 0 {
		return fallback
	}
	return limit
}

func cloneEnv(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func inferServerStatus(upstream mcp.Upstream) mcp.ServerStatus {
	copyUpstream := upstream
	copyUpstream.Status.ConnectionStatus = mcp.ConnectionStatusUnknown
	if !copyUpstream.Enabled {
		copyUpstream.Status.ConnectionStatus = mcp.ConnectionStatusDisabled
	}
	if copyUpstream.Mode == mcp.UpstreamModeHTTP {
		if strings.TrimSpace(copyUpstream.OAuthProviderID) != "" {
			switch copyUpstream.OAuthProviderID {
			case "bearer_token":
				copyUpstream.Status.AuthType = mcp.AuthTypeBearer
				if strings.TrimSpace(copyUpstream.AuthToken) == "" {
					copyUpstream.Status.AuthStatus = mcp.AuthStatusMissingCredentials
					action := "add a bearer token"
					copyUpstream.Status.ActionRequired = &action
				} else {
					copyUpstream.Status.AuthStatus = mcp.AuthStatusUnknown
				}
			case "custom_headers":
				copyUpstream.Status.AuthType = mcp.AuthTypeEnv
				copyUpstream.Status.AuthStatus = mcp.AuthStatusUnknown
			case "oauth_dcr":
				copyUpstream.Status.AuthType = mcp.AuthTypeHosted
				copyUpstream.Status.AuthStatus = mcp.AuthStatusMissingCredentials
				action := "dynamic client registration is not implemented yet"
				copyUpstream.Status.ActionRequired = &action
			default:
				copyUpstream.Status.AuthType = mcp.AuthTypeHosted
				copyUpstream.Status.AuthStatus = mcp.AuthStatusMissingCredentials
				action := "connect this server"
				copyUpstream.Status.ActionRequired = &action
			}
		} else if strings.TrimSpace(copyUpstream.AuthToken) != "" {
			copyUpstream.Status.AuthType = mcp.AuthTypeBearer
			copyUpstream.Status.AuthStatus = mcp.AuthStatusUnknown
		} else {
			copyUpstream.Status.AuthType = mcp.AuthTypeHosted
			copyUpstream.Status.AuthStatus = mcp.AuthStatusUnknown
		}
	} else {
		copyUpstream.Status.AuthType = mcp.AuthTypeNone
		for key, value := range copyUpstream.Env {
			upper := strings.ToUpper(key)
			if value != "" && (strings.Contains(upper, "TOKEN") || strings.Contains(upper, "KEY") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD")) {
				copyUpstream.Status.AuthType = mcp.AuthTypeEnv
				break
			}
		}
		copyUpstream.Status.AuthStatus = mcp.AuthStatusUnknown
	}
	if !copyUpstream.Enabled {
		copyUpstream.Status.ConnectionStatus = mcp.ConnectionStatusDisabled
		message := "enable the server to use it"
		copyUpstream.Status.ActionRequired = &message
	}
	return copyUpstream.Status
}

func (h *Handler) writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	body, _ := json.Marshal(result)
	writeJSON(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: id, Result: body})
}

func (h *Handler) forwardProxyEnvelope(ctx context.Context, server string, envelope mcp.Envelope, protocolVersion string) (mcp.ForwardResult, bool) {
	if h.forwarder == nil {
		return mcp.ForwardResult{}, false
	}
	resolver, ok := h.svc.(interface {
		ResolveContext(context.Context, string) (mcp.Upstream, error)
	})
	if !ok {
		return mcp.ForwardResult{}, false
	}
	upstream, err := resolver.ResolveContext(ctx, server)
	if err != nil {
		return mcp.ForwardResult{}, false
	}
	result, err := h.forwarder.ForwardEnvelope(ctx, upstream, envelope, protocolVersion)
	if err != nil {
		return mcp.ForwardResult{}, false
	}
	return result, true
}

func negotiateProtocolVersion(header string, params json.RawMessage) string {
	if v := normalizeProtocolVersion(header); v != "" {
		return v
	}
	var payload struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &payload); err == nil {
		if v := normalizeProtocolVersion(payload.ProtocolVersion); v != "" {
			return v
		}
	}
	return mcp.DefaultMCPProtocolVersion
}

func normalizeProtocolVersion(value string) string {
	switch strings.TrimSpace(value) {
	case mcp.MCPProtocolVersion2025:
		return mcp.MCPProtocolVersion2025
	case mcp.MCPProtocolVersion2024:
		return mcp.MCPProtocolVersion2024
	default:
		return ""
	}
}

func setMCPProtocolVersionHeader(w http.ResponseWriter, version string) {
	if strings.TrimSpace(version) == "" {
		return
	}
	w.Header().Set("MCP-Protocol-Version", version)
}

func (h *Handler) writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	errBody, _ := json.Marshal(map[string]any{"code": code, "message": message})
	writeJSON(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: id, Error: errBody})
}

func normalizeToolCallResult(raw json.RawMessage, isError bool) any {
	if len(raw) == 0 {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}, "isError": isError}
	}
	var existing map[string]any
	if err := json.Unmarshal(raw, &existing); err == nil {
		if _, ok := existing["content"]; ok {
			if _, has := existing["isError"]; !has && isError {
				existing["isError"] = true
			}
			return existing
		}
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(raw)}}, "isError": isError}
}

// recordInitializeClientInfo captures the MCP `initialize.clientInfo` so it
// can later be attached to invocations. It:
//   - Always stashes the snapshot in the in-memory session cache keyed by
//     `Mcp-Session-Id` (or remote-addr + UA fallback) so anonymous /mcp/
//     callers still get an Agent column populated for follow-up tools/call
//     requests on the same session.
//   - Additionally upserts into the agent_clients table when both an
//     authenticated agent_id and a configured recorder are present.
//
// Best-effort throughout: parse errors or repo failures must not break the
// initialize handshake.
func (h *Handler) recordInitializeClientInfo(r *http.Request, params json.RawMessage) {
	var payload struct {
		ClientInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &payload)
	}
	name := strings.TrimSpace(payload.ClientInfo.Name)
	version := strings.TrimSpace(payload.ClientInfo.Version)
	if name == "" && version == "" {
		return
	}
	snap := clientInfoSnapshot{Name: name, Version: version, At: time.Now().UTC()}
	if key := sessionKeyFromRequest(r); key != "" {
		h.clientInfoMu.Lock()
		h.clientInfoCache[key] = snap
		// Bound the cache so a noisy fleet of agents can't grow it
		// unboundedly. 1024 entries is plenty for a dev / single-tenant
		// instance; oldest evicted opportunistically.
		if len(h.clientInfoCache) > 1024 {
			var oldestKey string
			var oldestAt time.Time
			for k, v := range h.clientInfoCache {
				if oldestKey == "" || v.At.Before(oldestAt) {
					oldestKey, oldestAt = k, v.At
				}
			}
			delete(h.clientInfoCache, oldestKey)
		}
		h.clientInfoMu.Unlock()
	}
}

// lookupClientInfo returns any clientInfo captured for the request's session
// key (set on a prior `initialize` call). Used so that anonymous tools/call
// requests can still carry through "Amp 0.0.1234"-style context.
func (h *Handler) lookupClientInfo(r *http.Request) (clientInfoSnapshot, bool) {
	key := sessionKeyFromRequest(r)
	if key == "" {
		return clientInfoSnapshot{}, false
	}
	h.clientInfoMu.RLock()
	snap, ok := h.clientInfoCache[key]
	h.clientInfoMu.RUnlock()
	return snap, ok
}

// sessionKeyFromRequest produces a best-effort identifier for the agent
// session originating this HTTP request. Prefers the standard MCP
// `Mcp-Session-Id` header when the client sets it; falls back to remote
// IP + User-Agent which is usually distinct enough for local dev.
func sessionKeyFromRequest(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("Mcp-Session-Id")); v != "" {
		return "sid:" + v
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > -1 {
		host = host[:i]
	}
	ua := strings.TrimSpace(r.Header.Get("User-Agent"))
	if host == "" && ua == "" {
		return ""
	}
	return "addr:" + host + "|ua:" + ua
}

func compactRequestID(id json.RawMessage) string {
	if len(id) == 0 {
		return ""
	}
	return string(id)
}

func stringPtr(v string) *string { return &v }

func invocationSignature(items []invocation.InvocationResponse) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		completed := ""
		if item.CompletedAt != nil {
			completed = item.CompletedAt.UTC().Format(time.RFC3339Nano)
		}
		parts = append(parts, item.InvocationID+"|"+string(item.Status)+"|"+completed+"|"+item.Summary)
	}
	return strings.Join(parts, ",")
}

func mustJSONString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		scheme = proto
	}
	host := r.Host
	if forwardedHost := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwardedHost != "" {
		host = forwardedHost
	}
	return scheme + "://" + host
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func escapeHTMLString(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return replacer.Replace(value)
}

func (h *Handler) emitTraceEvent(ctx context.Context, server string, eventType string, payload map[string]any) error {
	if h.debug {
		log.Printf("[mcp] trace server=%s event=%s payload=%v", server, eventType, payload)
	}
	return nil
}

func (h *Handler) debugf(format string, args ...any) {
	if !h.debug {
		return
	}
	log.Printf("[mcp] "+format, args...)
}
