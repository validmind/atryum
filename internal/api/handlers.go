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
	"strconv"
	"strings"
	"time"

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
	Events(ctx context.Context, invocationID string, filter invocation.EventListFilter) (invocation.EventListResponse, error)
	Approve(ctx context.Context, invocationID string) error
	Deny(ctx context.Context, invocationID string, message string) error
	Submit(ctx context.Context, req invocation.ExternalSubmitRequest) (invocation.InvocationResponse, error)
	RecordExecution(ctx context.Context, invocationID string, update invocation.ExternalExecutionUpdate) (invocation.InvocationResponse, error)
}

type mcpEnvelopeForwarder interface {
	ForwardEnvelope(ctx context.Context, upstream mcp.Upstream, envelope mcp.Envelope, protocolVersion string) (mcp.ForwardResult, error)
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
}

type Handler struct {
	svc            service
	serverSvc      serverService
	policyRegistry *policy.Registry
	rulesRepo      rulesRepo
	forwarder      mcpEnvelopeForwarder
	staticHTTP     http.Handler
	debug          bool
}

type PolicyStatusResponse struct {
	ActiveProvider       string               `json:"active_provider"`
	DisplayName          string               `json:"display_name"`
	WindowExpiresAt      *time.Time           `json:"window_expires_at,omitempty"`
	TimedApproveFallback string               `json:"timed_approve_fallback,omitempty"`
	Providers            []PolicyProviderInfo `json:"providers"`
}

type PolicyProviderInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type PolicyUpdateRequest struct {
	Provider             string `json:"provider"`
	DurationMinutes      int    `json:"duration_minutes,omitempty"`
	TimedApproveFallback string `json:"timed_approve_fallback,omitempty"`
}

type AdminServer struct {
	Name               string            `json:"name"`
	Mode               string            `json:"mode"`
	BaseURL            string            `json:"base_url,omitempty"`
	AuthToken          string            `json:"auth_token,omitempty"`
	AuthHeaders        []mcp.AuthHeader  `json:"auth_headers,omitempty"`
	TimeoutSeconds     int               `json:"timeout_seconds"`
	Command            string            `json:"command,omitempty"`
	Args               []string          `json:"args,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
	Enabled            bool              `json:"enabled"`
	AuthType           string            `json:"auth_type"`
	ConnectionStatus   string            `json:"connection_status"`
	AuthStatus         string            `json:"auth_status"`
	ReauthNeeded       bool              `json:"reauth_needed"`
	LastCheckedAt      *time.Time        `json:"last_checked_at,omitempty"`
	LastCheckOK        bool              `json:"last_check_ok"`
	LastErrorSummary   *string           `json:"last_error_summary,omitempty"`
	ActionRequired     *string           `json:"action_required,omitempty"`
	OAuthProviderID    string            `json:"oauth_provider_id,omitempty"`
	OAuthProviderLabel string            `json:"oauth_provider_label,omitempty"`
}

type AdminServerUpsertRequest struct {
	Name           string            `json:"name,omitempty"`
	Mode           string            `json:"mode"`
	BaseURL        string            `json:"base_url,omitempty"`
	AuthToken      string            `json:"auth_token,omitempty"`
	AuthHeaders    []mcp.AuthHeader  `json:"auth_headers,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Enabled        *bool             `json:"enabled,omitempty"`
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
	ID             string    `json:"id"`
	Action         string    `json:"action"`
	ServerPatterns []string  `json:"server_patterns"`
	ToolPatterns   []string  `json:"tool_patterns"`
	UserPattern    string    `json:"user_pattern"`
	Description    string    `json:"description,omitempty"`
	Enabled        bool      `json:"enabled"`
	Order          int       `json:"order"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type AdminRuleInput struct {
	Action         string   `json:"action"`
	ServerPatterns []string `json:"server_patterns"`
	ToolPatterns   []string `json:"tool_patterns"`
	UserPattern    string   `json:"user_pattern"`
	Description    string   `json:"description,omitempty"`
	Enabled        *bool    `json:"enabled,omitempty"`
}

type RuleListResponse struct {
	Items []AdminRule `json:"items"`
}

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

func NewHandler(svc service, serverSvc serverService, policyRegistry *policy.Registry, rules rulesRepo) *Handler {
	staticSub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	debug := strings.EqualFold(os.Getenv("ATRYUM_MCP_DEBUG"), "1") || strings.EqualFold(os.Getenv("ATRYUM_MCP_DEBUG"), "true")
	var forwarder mcpEnvelopeForwarder
	if f, ok := svc.(mcpEnvelopeForwarder); ok {
		forwarder = f
	}
	return &Handler{svc: svc, serverSvc: serverSvc, policyRegistry: policyRegistry, rulesRepo: rules, forwarder: forwarder, staticHTTP: http.FileServer(http.FS(staticSub)), debug: debug}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/mcp/", h.invokeUpstream)
	mux.HandleFunc("/api/v1/invocations", h.invocations)
	mux.HandleFunc("/api/v1/admin/invocations", h.adminInvocations)
	mux.HandleFunc("/api/v1/admin/invocations/stream", h.adminInvocationStream)
	mux.HandleFunc("/api/v1/admin/invocations/", h.adminInvocationDetail)
	mux.HandleFunc("/api/v1/admin/servers", h.adminServers)
	mux.HandleFunc("/api/v1/admin/servers/", h.adminServerDetail)
	mux.HandleFunc("/api/v1/admin/rules", h.adminRules)
	mux.HandleFunc("/api/v1/admin/rules/", h.adminRuleDetail)
	mux.HandleFunc("/api/v1/admin/oauth/callback", h.oauthCallback)
	mux.HandleFunc("/api/v1/admin/policy", h.adminPolicy)
	mux.HandleFunc("/api/v1/external/invocations", h.externalInvocations)
	mux.HandleFunc("/api/v1/external/invocations/", h.externalInvocationDetail)
	mux.HandleFunc("/ui", h.uiIndex)
	mux.Handle("/ui/", http.StripPrefix("/ui/", h.staticHTTP))
	mux.HandleFunc("/", h.root)
	return mux
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
		result := map[string]any{
			"protocolVersion": protocolVersion,
			"serverInfo": map[string]any{
				"name":    "atryum",
				"version": "0.1.0",
			},
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
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
		_ = h.emitTraceEvent(r.Context(), server, "mcp.tools.list", map[string]any{"tool_count": len(tools), "request_id": requestID})
		h.writeRPCResult(w, req.ID, map[string]any{"tools": tools})
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			h.writeRPCError(w, req.ID, -32602, "invalid params")
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
		resp, err := h.svc.Invoke(r.Context(), toolReq)
		if err != nil {
			h.writeRPCError(w, req.ID, -32000, err.Error())
			return
		}
		tracePayload := map[string]any{"request_id": requestID, "status": resp.Status, "invocation_id": resp.InvocationID, "tool": params.Name}
		_ = h.emitTraceEvent(r.Context(), server, "mcp.tools.call", tracePayload)
		if len(resp.Error) > 0 {
			h.writeRPCResult(w, req.ID, normalizeToolCallResult(resp.Error, true))
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

func (h *Handler) adminInvocations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	filter := invocation.InvocationListFilter{
		Offset: readUintQuery(r, "offset", 0),
		Limit:  readUintQuery(r, "limit", 50),
		Server: strings.TrimSpace(r.URL.Query().Get("server")),
		Tool:   strings.TrimSpace(r.URL.Query().Get("tool")),
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
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
		Offset: 0,
		Limit:  readUintQuery(r, "limit", 50),
		Server: strings.TrimSpace(r.URL.Query().Get("server")),
		Tool:   strings.TrimSpace(r.URL.Query().Get("tool")),
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
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
		if err := h.svc.Approve(r.Context(), id); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if strings.HasSuffix(trimmed, "/deny") {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		id := strings.TrimSuffix(trimmed, "/deny")
		id = strings.TrimSuffix(id, "/")
		var body struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if err := h.svc.Deny(r.Context(), id, body.Message); err != nil {
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
		resp, err := h.serverSvc.Test(r.Context(), name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
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
		if req.DurationMinutes > 0 {
			if ws, ok := h.policyRegistry.Active().(policy.WindowSetter); ok {
				var fallback policy.Provider
				if req.TimedApproveFallback != "" {
					fallback, _ = h.policyRegistry.Get(req.TimedApproveFallback)
				}
				ws.SetWindow(time.Now().UTC().Add(time.Duration(req.DurationMinutes)*time.Minute), fallback)
			}
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
	if ws, ok := active.(policy.WindowSetter); ok {
		resp.WindowExpiresAt = ws.WindowExpiresAt()
		if fb := ws.FallbackProvider(); fb != nil {
			resp.TimedApproveFallback = fb.ID()
		}
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
			ID:             "rule_" + newUUID(),
			Action:         req.Action,
			ServerPatterns: normalizePatternSlice(req.ServerPatterns),
			ToolPatterns:   normalizePatternSlice(req.ToolPatterns),
			UserPattern:    defaultPattern(req.UserPattern),
			Description:    req.Description,
			Enabled:        enabled,
			Order:          order,
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
		existing.UserPattern = defaultPattern(req.UserPattern)
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

func toAdminRule(r store.Rule) AdminRule {
	sp := r.ServerPatterns
	if sp == nil {
		sp = []string{}
	}
	tp := r.ToolPatterns
	if tp == nil {
		tp = []string{}
	}
	return AdminRule{
		ID:             r.ID,
		Action:         r.Action,
		ServerPatterns: sp,
		ToolPatterns:   tp,
		UserPattern:    r.UserPattern,
		Description:    r.Description,
		Enabled:        r.Enabled,
		Order:          r.Order,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
}

func normalizePatternSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func validateRuleInput(req AdminRuleInput) error {
	switch req.Action {
	case "auto_approve", "auto_deny", "human_approval":
	default:
		return fmt.Errorf("action must be one of: auto_approve, auto_deny, human_approval")
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
	repo      serverRepo
	oauthRepo *store.OAuthRepo
	client    *mcp.Client
	timeout   time.Duration
}

type serverRepo interface {
	ListServers(ctx context.Context, filter mcp.ServerFilter) ([]mcp.Upstream, int, error)
	GetServerAny(ctx context.Context, name string) (mcp.Upstream, error)
	UpsertServer(ctx context.Context, upstream mcp.Upstream) error
	UpdateServerStatus(ctx context.Context, name string, status mcp.ServerStatus) error
	DeleteServer(ctx context.Context, name string) error
	DisableServer(ctx context.Context, name string) error
}

func NewServerAdminService(repo serverRepo, oauthRepo *store.OAuthRepo, client *mcp.Client, timeout time.Duration) *ServerAdminService {
	return &ServerAdminService{repo: repo, oauthRepo: oauthRepo, client: client, timeout: timeout}
}

func (s *ServerAdminService) List(ctx context.Context, filter mcp.ServerFilter) (ServerListResponse, error) {
	items, total, err := s.repo.ListServers(ctx, filter)
	if err != nil {
		return ServerListResponse{}, err
	}
	servers := make([]AdminServer, 0, len(items))
	for _, item := range items {
		servers = append(servers, toAdminServer(item))
	}
	return ServerListResponse{Items: servers, Total: total, Offset: filter.Offset, Limit: normalizeLimit(filter.Limit, 50)}, nil
}

func (s *ServerAdminService) Get(ctx context.Context, name string) (AdminServer, error) {
	upstream, err := s.repo.GetServerAny(ctx, name)
	if err != nil {
		return AdminServer{}, err
	}
	return toAdminServer(upstream), nil
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
	return toAdminServer(upstream), nil
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
	redirectURI := strings.TrimRight(appBaseURL, "/") + "/api/v1/admin/oauth/callback"
	connectReq, err := provider.BuildConnectRequest(ctx, upstream, redirectURI, stateToken)
	if err != nil {
		return OAuthConnectStartResponse{}, err
	}
	if err := s.oauthRepo.UpsertConnectSession(ctx, store.OAuthConnectSession{State: stateToken, ServerName: name, Status: "pending", CodeVerifier: connectReq.CodeVerifier, RedirectURI: redirectURI, StartedAt: time.Now().UTC()}); err != nil {
		return OAuthConnectStartResponse{}, err
	}
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
	return AdminServer{Name: upstream.Name, Mode: string(upstream.Mode), BaseURL: upstream.BaseURL, AuthToken: upstream.AuthToken, AuthHeaders: append([]mcp.AuthHeader(nil), upstream.AuthHeaders...), TimeoutSeconds: int(upstream.Timeout / time.Second), Command: upstream.Command, Args: append([]string(nil), upstream.Args...), Env: cloneEnv(upstream.Env), Enabled: upstream.Enabled, AuthType: string(upstream.Status.AuthType), ConnectionStatus: string(upstream.Status.ConnectionStatus), AuthStatus: string(upstream.Status.AuthStatus), ReauthNeeded: upstream.Status.ReauthNeeded, LastCheckedAt: upstream.Status.LastCheckedAt, LastCheckOK: upstream.Status.LastCheckOK, LastErrorSummary: upstream.Status.LastErrorSummary, ActionRequired: upstream.Status.ActionRequired, OAuthProviderID: upstream.OAuthProviderID, OAuthProviderLabel: upstream.OAuthProviderLabel}
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
		parts = append(parts, item.InvocationID+"|"+string(item.Status)+"|"+completed)
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
