package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"atryum/internal/store"
)

// claudeAgentsRepo is the minimal store interface the API handlers need.
// store.ClaudeAgentsRepo satisfies this.
type claudeAgentsRepo interface {
	CreateAgent(ctx context.Context, agent store.ClaudeAgent) error
	GetAgent(ctx context.Context, id string) (store.ClaudeAgent, error)
	GetAgentByName(ctx context.Context, name string) (store.ClaudeAgent, error)
	ListAgents(ctx context.Context) ([]store.ClaudeAgent, error)
	UpdateAgent(ctx context.Context, agent store.ClaudeAgent) error
	DeleteAgent(ctx context.Context, id string) error
	CreateSession(ctx context.Context, session store.ClaudeAgentSession) error
	ListSessionsByAgent(ctx context.Context, agentID string) ([]store.ClaudeAgentSession, error)
	DeleteSession(ctx context.Context, id string) error
}

// AdminClaudeAgent is the JSON shape returned by the admin API.
type AdminClaudeAgent struct {
	ID          string                    `json:"id"`
	Name        string                    `json:"name"`
	AgentID     string                    `json:"agent_id"`
	Description string                    `json:"description,omitempty"`
	Enabled     bool                      `json:"enabled"`
	CreatedAt   time.Time                 `json:"created_at"`
	UpdatedAt   time.Time                 `json:"updated_at"`
	Sessions    []AdminClaudeAgentSession `json:"sessions,omitempty"`
}

type AdminClaudeAgentSession struct {
	ID              string    `json:"id"`
	SessionID       string    `json:"session_id"`
	LastEventCursor string    `json:"last_event_cursor,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type AdminClaudeAgentInput struct {
	Name        string `json:"name"`
	AgentID     string `json:"agent_id"`
	Description string `json:"description,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

type AdminClaudeAgentSessionInput struct {
	SessionID string `json:"session_id"`
}

type ClaudeAgentListResponse struct {
	Items []AdminClaudeAgent `json:"items"`
}

func (h *Handler) adminClaudeAgents(w http.ResponseWriter, r *http.Request) {
	if h.claudeAgentsRepo == nil {
		writeError(w, http.StatusNotImplemented, "claude agents not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		agents, err := h.claudeAgentsRepo.ListAgents(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		items := make([]AdminClaudeAgent, 0, len(agents))
		for _, agent := range agents {
			sessions, err := h.claudeAgentsRepo.ListSessionsByAgent(r.Context(), agent.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			items = append(items, toAdminClaudeAgent(agent, sessions))
		}
		writeJSON(w, http.StatusOK, ClaudeAgentListResponse{Items: items})
	case http.MethodPost:
		var req AdminClaudeAgentInput
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if err := validateClaudeAgentInput(req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		agent := store.ClaudeAgent{
			ID:          "cagent_" + newUUID(),
			Name:        req.Name,
			AgentID:     req.AgentID,
			Description: req.Description,
			Enabled:     enabled,
		}
		if err := h.claudeAgentsRepo.CreateAgent(r.Context(), agent); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		created, err := h.claudeAgentsRepo.GetAgent(r.Context(), agent.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, toAdminClaudeAgent(created, nil))
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) adminClaudeAgentDetail(w http.ResponseWriter, r *http.Request) {
	if h.claudeAgentsRepo == nil {
		writeError(w, http.StatusNotImplemented, "claude agents not configured")
		return
	}
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/claude-agents/")
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	// Sub-routes for sessions: /{agentID}/sessions  and  /{agentID}/sessions/{sessionRowID}
	if idx := strings.Index(trimmed, "/sessions"); idx != -1 {
		agentID := strings.Trim(trimmed[:idx], "/")
		rest := strings.Trim(trimmed[idx+len("/sessions"):], "/")
		h.adminClaudeAgentSessions(w, r, agentID, rest)
		return
	}

	id := trimmed
	switch r.Method {
	case http.MethodGet:
		agent, err := h.claudeAgentsRepo.GetAgent(r.Context(), id)
		if err != nil {
			status := http.StatusInternalServerError
			if err == sql.ErrNoRows {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error())
			return
		}
		sessions, err := h.claudeAgentsRepo.ListSessionsByAgent(r.Context(), agent.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, toAdminClaudeAgent(agent, sessions))
	case http.MethodPut:
		var req AdminClaudeAgentInput
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if err := validateClaudeAgentInput(req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		existing, err := h.claudeAgentsRepo.GetAgent(r.Context(), id)
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
		existing.Name = req.Name
		existing.AgentID = req.AgentID
		existing.Description = req.Description
		existing.Enabled = enabled
		if err := h.claudeAgentsRepo.UpdateAgent(r.Context(), existing); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		updated, err := h.claudeAgentsRepo.GetAgent(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		sessions, err := h.claudeAgentsRepo.ListSessionsByAgent(r.Context(), updated.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, toAdminClaudeAgent(updated, sessions))
	case http.MethodDelete:
		if err := h.claudeAgentsRepo.DeleteAgent(r.Context(), id); err != nil {
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

func (h *Handler) adminClaudeAgentSessions(w http.ResponseWriter, r *http.Request, agentID, rest string) {
	if agentID == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	// Confirm parent agent exists.
	if _, err := h.claudeAgentsRepo.GetAgent(r.Context(), agentID); err != nil {
		status := http.StatusInternalServerError
		if err == sql.ErrNoRows {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}

	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			sessions, err := h.claudeAgentsRepo.ListSessionsByAgent(r.Context(), agentID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			items := make([]AdminClaudeAgentSession, 0, len(sessions))
			for _, s := range sessions {
				items = append(items, toAdminClaudeAgentSession(s))
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": items})
		case http.MethodPost:
			var req AdminClaudeAgentSessionInput
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid json")
				return
			}
			if strings.TrimSpace(req.SessionID) == "" {
				writeError(w, http.StatusBadRequest, "session_id is required")
				return
			}
			session := store.ClaudeAgentSession{
				ID:        "cagsess_" + newUUID(),
				AgentID:   agentID,
				SessionID: strings.TrimSpace(req.SessionID),
			}
			if err := h.claudeAgentsRepo.CreateSession(r.Context(), session); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusCreated, toAdminClaudeAgentSession(session))
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	// rest is a session row id.
	switch r.Method {
	case http.MethodDelete:
		if err := h.claudeAgentsRepo.DeleteSession(r.Context(), rest); err != nil {
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

func validateClaudeAgentInput(req AdminClaudeAgentInput) error {
	if strings.TrimSpace(req.Name) == "" {
		return errBadInput("name is required")
	}
	if strings.TrimSpace(req.AgentID) == "" {
		return errBadInput("agent_id is required")
	}
	return nil
}

type errBadInput string

func (e errBadInput) Error() string { return string(e) }

func toAdminClaudeAgent(a store.ClaudeAgent, sessions []store.ClaudeAgentSession) AdminClaudeAgent {
	out := AdminClaudeAgent{
		ID:          a.ID,
		Name:        a.Name,
		AgentID:     a.AgentID,
		Description: a.Description,
		Enabled:     a.Enabled,
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
	}
	if len(sessions) > 0 {
		out.Sessions = make([]AdminClaudeAgentSession, 0, len(sessions))
		for _, s := range sessions {
			out.Sessions = append(out.Sessions, toAdminClaudeAgentSession(s))
		}
	}
	return out
}

func toAdminClaudeAgentSession(s store.ClaudeAgentSession) AdminClaudeAgentSession {
	return AdminClaudeAgentSession{
		ID:              s.ID,
		SessionID:       s.SessionID,
		LastEventCursor: s.LastEventCursor,
		CreatedAt:       s.CreatedAt,
	}
}
