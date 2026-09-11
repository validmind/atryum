package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/store"
	"github.com/validmind/atryum/pkg/authz"
)

// MeResponse describes the authenticated principal behind an operator request.
type MeResponse struct {
	UserID  string `json:"user_id,omitempty"`
	Issuer  string `json:"issuer,omitempty"`
	Subject string `json:"subject,omitempty"`
	Email   string `json:"email,omitempty"`
	Name    string `json:"name,omitempty"`
	Role    string `json:"role"`
	Method  string `json:"method"`
	// AgentIDs lists the agents the user is a member of. Admins get an empty
	// list here because they may see every agent (use GET /api/v1/agents).
	AgentIDs []string `json:"agent_ids"`
}

// OperatorUser is the admin-facing view of a users row.
type OperatorUser struct {
	ID          string     `json:"id"`
	Issuer      string     `json:"issuer"`
	Subject     string     `json:"subject"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	DisabledAt  *time.Time `json:"disabled_at,omitempty"`
	Disabled    bool       `json:"disabled"`
}

type UserListResponse struct {
	Items []OperatorUser `json:"items"`
}

// OperatorUserInput is the PATCH body for /api/v1/users/{id}.
type OperatorUserInput struct {
	Disabled *bool   `json:"disabled,omitempty"`
	Role     *string `json:"role,omitempty"`
}

// OperatorAPIKey is the listing view of an api_keys row. Token is only ever
// populated in the response to the POST that created the key.
type OperatorAPIKey struct {
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	// AgentName is populated on per-user listings, where rows span agents.
	AgentName  string     `json:"agent_name,omitempty"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	CreatedBy  string     `json:"created_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	RevokedBy  string     `json:"revoked_by,omitempty"`
	Active     bool       `json:"active"`
	Token      string     `json:"token,omitempty"`
}

type APIKeyListResponse struct {
	Items []OperatorAPIKey `json:"items"`
}

// OperatorAPIKeyCreateInput is the POST body for /api/v1/agents/{id}/keys.
type OperatorAPIKeyCreateInput struct {
	Name string `json:"name"`
	// ExpiresIn is an optional lifetime such as "720h" or "30d". Omit for a
	// key that only dies by revocation.
	ExpiresIn string `json:"expires_in,omitempty"`
}

// OperatorAgentMember is one row of an agent's membership list.
type OperatorAgentMember struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

type AgentMemberListResponse struct {
	Items []OperatorAgentMember `json:"items"`
}

// OperatorUserAgent is one row of a user's agent list: an agent the user is a
// member of, with the agent's display fields joined.
type OperatorUserAgent struct {
	AgentID   string    `json:"agent_id"`
	AgentName string    `json:"agent_name"`
	Enabled   bool      `json:"enabled"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

type UserAgentListResponse struct {
	Items []OperatorUserAgent `json:"items"`
}

// OperatorAgentMemberInput is the POST body for /api/v1/agents/{id}/members.
type OperatorAgentMemberInput struct {
	UserID string `json:"user_id"`
}

const maxAPIKeyNameChars = 120

// me reports the current principal. GET /api/v1/me
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	p, ok := authz.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	resp := MeResponse{
		UserID:   p.UserID,
		Issuer:   p.Issuer,
		Subject:  p.Subject,
		Email:    p.Email,
		Name:     p.Name,
		Role:     string(p.Role),
		Method:   string(p.Method),
		AgentIDs: []string{},
	}
	if !p.IsAdmin() && p.UserID != "" && h.agentMembersRepo != nil {
		ids, err := h.agentMembersRepo.AgentIDsForUser(r.Context(), p.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list memberships")
			return
		}
		if ids != nil {
			resp.AgentIDs = ids
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// filterAgentsForPrincipal narrows an agent list to what the principal may
// see: everything for admins, member agents otherwise. Returns ok=false after
// writing an error response.
func (h *Handler) filterAgentsForPrincipal(w http.ResponseWriter, r *http.Request, records []store.AgentRecord) ([]store.AgentRecord, bool) {
	p, ok := authz.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return nil, false
	}
	if p.IsAdmin() {
		return records, true
	}
	if p.UserID == "" || h.agentMembersRepo == nil {
		return []store.AgentRecord{}, true
	}
	ids, err := h.agentMembersRepo.AgentIDsForUser(r.Context(), p.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list memberships")
		return nil, false
	}
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	out := make([]store.AgentRecord, 0, len(ids))
	for _, rec := range records {
		if _, ok := allowed[rec.ID]; ok {
			out = append(out, rec)
		}
	}
	return out, true
}

// operatorUsers handles GET /api/v1/users (admin).
func (h *Handler) operatorUsers(w http.ResponseWriter, r *http.Request) {
	if h.usersRepo == nil {
		writeError(w, http.StatusServiceUnavailable, "user store not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	users, err := h.usersRepo.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list users")
		return
	}
	items := make([]OperatorUser, 0, len(users))
	for _, u := range users {
		items = append(items, toOperatorUser(u))
	}
	writeJSON(w, http.StatusOK, UserListResponse{Items: items})
}

// operatorUserDetail handles GET/PATCH /api/v1/users/{id} (admin). PATCH with
// {"disabled": true} is the cascade point: it disables the user, revokes every
// key they created and drops their memberships.
func (h *Handler) operatorUserDetail(w http.ResponseWriter, r *http.Request) {
	if h.usersRepo == nil {
		writeError(w, http.StatusServiceUnavailable, "user store not configured")
		return
	}
	id, sub, _ := strings.Cut(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/users/"), "/"), "/")
	if id == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	switch sub {
	case "":
	case "agents":
		h.userAgents(w, r, id)
		return
	case "keys":
		h.userKeys(w, r, id)
		return
	default:
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		u, err := h.usersRepo.Get(r.Context(), id)
		if err != nil {
			writeUserLookupError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toOperatorUser(u))

	case http.MethodPatch:
		var req OperatorUserInput
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if req.Disabled == nil && req.Role == nil {
			writeError(w, http.StatusBadRequest, "nothing to update")
			return
		}
		actor, _ := authz.PrincipalFromContext(r.Context())
		if _, err := h.usersRepo.Get(r.Context(), id); err != nil {
			writeUserLookupError(w, err)
			return
		}
		if req.Role != nil {
			role := strings.ToLower(strings.TrimSpace(*req.Role))
			if role != store.UserRoleAdmin && role != store.UserRoleMember {
				writeError(w, http.StatusBadRequest, "role must be admin or member")
				return
			}
			if err := h.usersRepo.SetRole(r.Context(), id, role); err != nil {
				writeUserLookupError(w, err)
				return
			}
		}
		if req.Disabled != nil {
			if *req.Disabled && actor.UserID == id {
				writeError(w, http.StatusBadRequest, "cannot disable yourself")
				return
			}
			if err := h.usersRepo.SetDisabled(r.Context(), id, *req.Disabled); err != nil {
				writeUserLookupError(w, err)
				return
			}
			if *req.Disabled {
				if h.apiKeysRepo != nil {
					if _, err := h.apiKeysRepo.RevokeByCreator(r.Context(), id, actor.UserID); err != nil {
						writeError(w, http.StatusInternalServerError, "user disabled but key revocation failed")
						return
					}
				}
				if h.agentMembersRepo != nil {
					if err := h.agentMembersRepo.RemoveAllForUser(r.Context(), id); err != nil {
						writeError(w, http.StatusInternalServerError, "user disabled but membership removal failed")
						return
					}
				}
			}
		}
		u, err := h.usersRepo.Get(r.Context(), id)
		if err != nil {
			writeUserLookupError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toOperatorUser(u))

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// userAgents handles GET /api/v1/users/{id}/agents (admin): the agents the
// user is a member of. Membership itself is edited from the agent side
// (/api/v1/agents/{id}/members), so this view is read-only.
func (h *Handler) userAgents(w http.ResponseWriter, r *http.Request, userID string) {
	if h.agentMembersRepo == nil {
		writeError(w, http.StatusServiceUnavailable, "user store not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := h.usersRepo.Get(r.Context(), userID); err != nil {
		writeUserLookupError(w, err)
		return
	}
	members, err := h.agentMembersRepo.ListByUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list memberships")
		return
	}
	items := make([]OperatorUserAgent, 0, len(members))
	for _, m := range members {
		items = append(items, OperatorUserAgent{
			AgentID:   m.AgentID,
			AgentName: m.AgentName,
			Enabled:   m.AgentEnabled,
			Role:      m.Role,
			CreatedAt: m.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, UserAgentListResponse{Items: items})
}

// userKeys handles GET /api/v1/users/{id}/keys (admin): every API key the
// user issued, across agents, newest first. Revocation goes through the
// agent-scoped DELETE /api/v1/agents/{agent}/keys/{key}.
func (h *Handler) userKeys(w http.ResponseWriter, r *http.Request, userID string) {
	if h.apiKeysRepo == nil {
		writeError(w, http.StatusServiceUnavailable, "api key store not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := h.usersRepo.Get(r.Context(), userID); err != nil {
		writeUserLookupError(w, err)
		return
	}
	keys, err := h.apiKeysRepo.ListByCreator(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list keys")
		return
	}
	agentNames := map[string]string{}
	if len(keys) > 0 {
		records, err := h.agentsRepo.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list agents")
			return
		}
		for _, a := range records {
			agentNames[a.ID] = a.VMName
		}
	}
	now := time.Now()
	items := make([]OperatorAPIKey, 0, len(keys))
	for _, k := range keys {
		item := toOperatorAPIKey(k, now, "")
		item.AgentName = agentNames[k.AgentID]
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, APIKeyListResponse{Items: items})
}

// agentKeys handles /api/v1/agents/{id}/keys and /keys/{key_id}. Members of
// the agent (and admins) may list, create and revoke keys.
func (h *Handler) agentKeys(w http.ResponseWriter, r *http.Request, agentID, keyID string) {
	if h.apiKeysRepo == nil {
		writeError(w, http.StatusServiceUnavailable, "api key store not configured")
		return
	}
	if agentID == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !h.can(w, r, authz.ActionAgentKeysManage, authz.Resource{AgentID: agentID}) {
		return
	}
	if _, err := h.agentsRepo.Get(r.Context(), agentID); err != nil {
		status := http.StatusInternalServerError
		if err == sql.ErrNoRows {
			status = http.StatusNotFound
		}
		writeError(w, status, "agent not found")
		return
	}
	principal, _ := authz.PrincipalFromContext(r.Context())

	if keyID != "" {
		if strings.Contains(keyID, "/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		key, err := h.apiKeysRepo.Get(r.Context(), keyID)
		if err != nil || key.AgentID != agentID {
			writeError(w, http.StatusNotFound, "key not found")
			return
		}
		if err := h.apiKeysRepo.Revoke(r.Context(), keyID, principal.UserID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to revoke key")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch r.Method {
	case http.MethodGet:
		keys, err := h.apiKeysRepo.ListByAgent(r.Context(), agentID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list keys")
			return
		}
		now := time.Now()
		items := make([]OperatorAPIKey, 0, len(keys))
		for _, k := range keys {
			items = append(items, toOperatorAPIKey(k, now, ""))
		}
		writeJSON(w, http.StatusOK, APIKeyListResponse{Items: items})

	case http.MethodPost:
		var req OperatorAPIKeyCreateInput
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		name := strings.TrimSpace(req.Name)
		if len([]rune(name)) > maxAPIKeyNameChars {
			writeError(w, http.StatusBadRequest, "name is too long")
			return
		}
		var expiresAt *time.Time
		if strings.TrimSpace(req.ExpiresIn) != "" {
			d, err := parseKeyLifetime(req.ExpiresIn)
			if err != nil {
				writeError(w, http.StatusBadRequest, "expires_in must be a duration such as 720h or 30d")
				return
			}
			t := time.Now().UTC().Add(d)
			expiresAt = &t
		}
		token, hash, prefix, err := auth.GenerateAgentKey()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to generate key")
			return
		}
		created, err := h.apiKeysRepo.Create(r.Context(), store.APIKey{
			AgentID:   agentID,
			CreatedBy: principal.UserID,
			Name:      name,
			KeyHash:   hash,
			KeyPrefix: prefix,
			ExpiresAt: expiresAt,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create key")
			return
		}
		writeJSON(w, http.StatusCreated, toOperatorAPIKey(created, time.Now(), token))

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// agentMembers handles /api/v1/agents/{id}/members and /members/{user_id}.
// Members may list; only admins may add or remove (ActionAgentMembersManage).
func (h *Handler) agentMembers(w http.ResponseWriter, r *http.Request, agentID, userID string) {
	if h.agentMembersRepo == nil || h.usersRepo == nil {
		writeError(w, http.StatusServiceUnavailable, "user store not configured")
		return
	}
	if agentID == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if _, err := h.agentsRepo.Get(r.Context(), agentID); err != nil {
		status := http.StatusInternalServerError
		if err == sql.ErrNoRows {
			status = http.StatusNotFound
		}
		writeError(w, status, "agent not found")
		return
	}
	actor, _ := authz.PrincipalFromContext(r.Context())

	if userID != "" {
		if strings.Contains(userID, "/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !h.can(w, r, authz.ActionAgentMembersManage, authz.Resource{AgentID: agentID}) {
			return
		}
		if err := h.agentMembersRepo.Remove(r.Context(), agentID, userID); err != nil {
			if err == sql.ErrNoRows {
				writeError(w, http.StatusNotFound, "member not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to remove member")
			return
		}
		// Cascade: the departing member's keys on this agent stop working.
		if h.apiKeysRepo != nil {
			if _, err := h.apiKeysRepo.RevokeByCreatorForAgent(r.Context(), userID, agentID, actor.UserID); err != nil {
				writeError(w, http.StatusInternalServerError, "member removed but key revocation failed")
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch r.Method {
	case http.MethodGet:
		if !h.can(w, r, authz.ActionAgentRead, authz.Resource{AgentID: agentID}) {
			return
		}
		members, err := h.agentMembersRepo.ListByAgent(r.Context(), agentID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list members")
			return
		}
		items := make([]OperatorAgentMember, 0, len(members))
		for _, m := range members {
			items = append(items, OperatorAgentMember{UserID: m.UserID, Email: m.UserEmail, Name: m.UserName, Role: m.Role, CreatedAt: m.CreatedAt})
		}
		writeJSON(w, http.StatusOK, AgentMemberListResponse{Items: items})

	case http.MethodPost:
		if !h.can(w, r, authz.ActionAgentMembersManage, authz.Resource{AgentID: agentID}) {
			return
		}
		var req OperatorAgentMemberInput
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		uid := strings.TrimSpace(req.UserID)
		if uid == "" {
			writeError(w, http.StatusBadRequest, "user_id is required")
			return
		}
		u, err := h.usersRepo.Get(r.Context(), uid)
		if err != nil {
			writeUserLookupError(w, err)
			return
		}
		if u.Disabled() {
			writeError(w, http.StatusConflict, "user is disabled")
			return
		}
		if err := h.agentMembersRepo.Add(r.Context(), agentID, uid, store.AgentMemberRoleOwner); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to add member")
			return
		}
		members, err := h.agentMembersRepo.ListByAgent(r.Context(), agentID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list members")
			return
		}
		items := make([]OperatorAgentMember, 0, len(members))
		for _, m := range members {
			items = append(items, OperatorAgentMember{UserID: m.UserID, Email: m.UserEmail, Name: m.UserName, Role: m.Role, CreatedAt: m.CreatedAt})
		}
		writeJSON(w, http.StatusCreated, AgentMemberListResponse{Items: items})

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func toOperatorUser(u store.User) OperatorUser {
	return OperatorUser{
		ID:          u.ID,
		Issuer:      u.Issuer,
		Subject:     u.Subject,
		Email:       u.Email,
		Name:        u.Name,
		Role:        u.Role,
		CreatedAt:   u.CreatedAt,
		LastLoginAt: u.LastLoginAt,
		DisabledAt:  u.DisabledAt,
		Disabled:    u.Disabled(),
	}
}

func toOperatorAPIKey(k store.APIKey, now time.Time, token string) OperatorAPIKey {
	return OperatorAPIKey{
		ID:         k.ID,
		AgentID:    k.AgentID,
		Name:       k.Name,
		KeyPrefix:  k.KeyPrefix,
		CreatedBy:  k.CreatedBy,
		CreatedAt:  k.CreatedAt,
		ExpiresAt:  k.ExpiresAt,
		LastUsedAt: k.LastUsedAt,
		RevokedAt:  k.RevokedAt,
		RevokedBy:  k.RevokedBy,
		Active:     k.Active(now),
		Token:      token,
	}
}

func writeUserLookupError(w http.ResponseWriter, err error) {
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "user lookup failed")
}

// parseKeyLifetime accepts Go durations plus a "d" (days) suffix.
func parseKeyLifetime(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	var d time.Duration
	var err error
	if strings.HasSuffix(raw, "d") {
		d, err = time.ParseDuration(strings.TrimSuffix(raw, "d") + "h")
		d *= 24
	} else {
		d, err = time.ParseDuration(raw)
	}
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, errors.New("lifetime must be positive")
	}
	return d, nil
}
