package api

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"atryum/internal/invocation"
	"atryum/internal/mcp"
)

//go:embed web/*
var webFS embed.FS

type service interface {
	Invoke(ctx context.Context, req invocation.CreateInvocationRequest) (invocation.InvocationResponse, error)
	Get(ctx context.Context, id string) (invocation.InvocationResponse, error)
	List(ctx context.Context, filter invocation.InvocationListFilter) (invocation.InvocationListResponse, error)
	Events(ctx context.Context, invocationID string, filter invocation.EventListFilter) (invocation.EventListResponse, error)
}

type serverService interface {
	List(ctx context.Context, filter mcp.ServerFilter) (ServerListResponse, error)
	Get(ctx context.Context, name string) (AdminServer, error)
	Upsert(ctx context.Context, name string, req AdminServerUpsertRequest) (AdminServer, error)
	Delete(ctx context.Context, name string, disable bool) error
	Test(ctx context.Context, name string) (ServerTestResponse, error)
}

type Handler struct {
	svc        service
	serverSvc  serverService
	staticHTTP http.Handler
}

type AdminServer struct {
	Name           string            `json:"name"`
	Mode           string            `json:"mode"`
	BaseURL        string            `json:"base_url,omitempty"`
	AuthToken      string            `json:"auth_token,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Enabled        bool              `json:"enabled"`
}

type AdminServerUpsertRequest struct {
	Name           string            `json:"name,omitempty"`
	Mode           string            `json:"mode"`
	BaseURL        string            `json:"base_url,omitempty"`
	AuthToken      string            `json:"auth_token,omitempty"`
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
	Ok      bool   `json:"ok"`
	Message string `json:"message"`
}

func NewHandler(svc service, serverSvc serverService) *Handler {
	staticSub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	return &Handler{svc: svc, serverSvc: serverSvc, staticHTTP: http.FileServer(http.FS(staticSub))}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/mcp/", h.invokeUpstream)
	mux.HandleFunc("/api/v1/invocations", h.invocations)
	mux.HandleFunc("/api/v1/admin/invocations", h.adminInvocations)
	mux.HandleFunc("/api/v1/admin/invocations/", h.adminInvocationDetail)
	mux.HandleFunc("/api/v1/admin/servers", h.adminServers)
	mux.HandleFunc("/api/v1/admin/servers/", h.adminServerDetail)
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
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	server := strings.TrimPrefix(r.URL.Path, "/mcp/")
	server = strings.Trim(server, "/")
	if server == "" {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	h.handleInvocation(w, r, server)
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

func (h *Handler) adminInvocationDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/invocations/")
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if strings.HasSuffix(trimmed, "/events") {
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": message}})
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
	repo    serverRepo
	timeout time.Duration
}

type serverRepo interface {
	ListServers(ctx context.Context, filter mcp.ServerFilter) ([]mcp.Upstream, int, error)
	GetServerAny(ctx context.Context, name string) (mcp.Upstream, error)
	UpsertServer(ctx context.Context, upstream mcp.Upstream) error
	DeleteServer(ctx context.Context, name string) error
	DisableServer(ctx context.Context, name string) error
}

func NewServerAdminService(repo serverRepo, timeout time.Duration) *ServerAdminService {
	return &ServerAdminService{repo: repo, timeout: timeout}
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
		Name:      serverName,
		Mode:      mcp.UpstreamMode(mode),
		BaseURL:   strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"),
		AuthToken: req.AuthToken,
		Timeout:   time.Duration(defaultIfZero(req.TimeoutSeconds, 30)) * time.Second,
		Command:   strings.TrimSpace(req.Command),
		Args:      append([]string(nil), req.Args...),
		Env:       cloneEnv(req.Env),
		Enabled:   enabled,
	}
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
	if !upstream.Enabled {
		return ServerTestResponse{Ok: false, Message: "server is disabled"}, nil
	}
	testCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	switch upstream.Mode {
	case mcp.UpstreamModeHTTP:
		req, err := http.NewRequestWithContext(testCtx, http.MethodHead, upstream.BaseURL, nil)
		if err != nil {
			return ServerTestResponse{}, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return ServerTestResponse{Ok: false, Message: err.Error()}, nil
		}
		_ = resp.Body.Close()
		return ServerTestResponse{Ok: true, Message: fmt.Sprintf("http %d", resp.StatusCode)}, nil
	case mcp.UpstreamModeStdio:
		if strings.TrimSpace(upstream.Command) == "" {
			return ServerTestResponse{Ok: false, Message: "missing command"}, nil
		}
		return ServerTestResponse{Ok: true, Message: "stdio configuration looks valid"}, nil
	default:
		return ServerTestResponse{Ok: false, Message: "unsupported mode"}, nil
	}
}

func toAdminServer(upstream mcp.Upstream) AdminServer {
	return AdminServer{
		Name:           upstream.Name,
		Mode:           string(upstream.Mode),
		BaseURL:        upstream.BaseURL,
		AuthToken:      upstream.AuthToken,
		TimeoutSeconds: int(upstream.Timeout / time.Second),
		Command:        upstream.Command,
		Args:           append([]string(nil), upstream.Args...),
		Env:            cloneEnv(upstream.Env),
		Enabled:        upstream.Enabled,
	}
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
