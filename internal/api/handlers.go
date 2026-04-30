package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"atryum/internal/invocation"
)

type service interface {
	Invoke(ctx context.Context, req invocation.CreateInvocationRequest) (invocation.InvocationResponse, error)
	Get(ctx context.Context, id string) (invocation.InvocationResponse, error)
	List(ctx context.Context, limit uint64) ([]invocation.InvocationResponse, error)
	Events(ctx context.Context, invocationID string) ([]invocation.EventResponse, error)
}

type Handler struct{ svc service }

func NewHandler(svc service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/mcp/", h.invokeUpstream)
	mux.HandleFunc("/api/v1/invocations", h.invocations)
	mux.HandleFunc("/api/v1/admin/invocations", h.adminInvocations)
	mux.HandleFunc("/api/v1/admin/invocations/", h.adminInvocationDetail)
	return mux
}

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
	limit := uint64(50)
	if q := r.URL.Query().Get("limit"); q != "" {
		if parsed, err := strconv.ParseUint(q, 10, 64); err == nil {
			limit = parsed
		}
	}
	resp, err := h.svc.List(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp})
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
		events, err := h.svc.Events(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": events})
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": message}})
}
