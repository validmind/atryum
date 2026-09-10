package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/store"
	_ "modernc.org/sqlite"
)

type identityRig struct {
	t       *testing.T
	rig     *authTestRig
	h       http.Handler
	users   *store.UsersRepo
	members *store.AgentMembersRepo
	keys    *store.APIKeysRepo
	agents  *store.AgentsRepo
}

func newIdentityRig(t *testing.T) *identityRig {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "identity.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	rig := newAuthTestRig(t, func(c *auth.Config) {
		c.AdminEnabled = true
		c.AdminClientID = "admin-client"
		c.AdminClaim = "atryum_admin"
		c.AdminClaimValue = "true"
	})
	users := store.NewUsersRepo(db)
	members := store.NewAgentMembersRepo(db)
	keys := store.NewAPIKeysRepo(db)
	agents := store.NewAgentsRepo(db)
	h := NewHandler(&stubService{}, stubServerService{}, nil, &stubRulesRepo{}, agents, nil, nil, nil, nil, nil)
	h.SetAuthValidator(rig.v)
	h.SetIdentityStores(users, members, keys)
	h.SetAgentKeyResolver(NewAgentKeyResolver(keys, agents))
	h.SetAuthz(nil, NewUserProvisioner(users))
	return &identityRig{t: t, rig: rig, h: h.Routes(), users: users, members: members, keys: keys, agents: agents}
}

func (r *identityRig) token(subject string, admin bool) string {
	claims := defaultClaims()
	claims["sub"] = subject
	claims["email"] = subject + "@example.com"
	claims["name"] = strings.ToUpper(subject)
	if admin {
		claims["atryum_admin"] = true
	}
	return r.rig.sign(r.t, claims)
}

func (r *identityRig) do(method, path, bearer, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return out
}

func TestAgentAPIKeysEndToEnd(t *testing.T) {
	r := newIdentityRig(t)
	admin := r.token("admin-1", true)
	member := r.token("member-1", false)

	// Admin creates two agents. Admin principals do carry a user row, so the
	// admin becomes a member of both; that must not matter for admin access.
	w := r.do(http.MethodPost, "/api/v1/agents", admin, `{"name":"Agent A","enabled":true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create A: %d %s", w.Code, w.Body.String())
	}
	agentA := decode[OperatorAgent](t, w).CUID
	w = r.do(http.MethodPost, "/api/v1/agents", admin, `{"name":"Agent B","enabled":true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create B: %d %s", w.Code, w.Body.String())
	}
	agentB := decode[OperatorAgent](t, w).CUID

	// Member is provisioned on first login and sees nothing yet.
	w = r.do(http.MethodGet, "/api/v1/me", member, "")
	if w.Code != http.StatusOK {
		t.Fatalf("me: %d %s", w.Code, w.Body.String())
	}
	me := decode[MeResponse](t, w)
	if me.Role != "member" || me.UserID == "" || me.Email != "member-1@example.com" || len(me.AgentIDs) != 0 {
		t.Fatalf("unexpected me: %+v", me)
	}
	w = r.do(http.MethodGet, "/api/v1/agents", member, "")
	if w.Code != http.StatusOK || len(decode[AgentListResponse](t, w).Items) != 0 {
		t.Fatalf("member should see no agents yet: %d %s", w.Code, w.Body.String())
	}
	if w = r.do(http.MethodPost, "/api/v1/agents/"+agentA+"/keys", member, `{"name":"laptop"}`); w.Code != http.StatusForbidden {
		t.Fatalf("non-member key issue should be 403, got %d %s", w.Code, w.Body.String())
	}
	if w = r.do(http.MethodGet, "/api/v1/agents/"+agentA, member, ""); w.Code != http.StatusForbidden {
		t.Fatalf("non-member agent read should be 403, got %d", w.Code)
	}
	if w = r.do(http.MethodGet, "/api/v1/users", member, ""); w.Code != http.StatusForbidden {
		t.Fatalf("member listing users should be 403, got %d", w.Code)
	}
	if w = r.do(http.MethodGet, "/api/v1/servers", member, ""); w.Code != http.StatusForbidden {
		t.Fatalf("member on legacy operator route should be 403, got %d", w.Code)
	}

	// Admin adds the member to agent A. Members cannot manage membership.
	if w = r.do(http.MethodPost, "/api/v1/agents/"+agentA+"/members", member, `{"user_id":"`+me.UserID+`"}`); w.Code != http.StatusForbidden {
		t.Fatalf("member adding member should be 403, got %d", w.Code)
	}
	w = r.do(http.MethodPost, "/api/v1/agents/"+agentA+"/members", admin, `{"user_id":"`+me.UserID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("add member: %d %s", w.Code, w.Body.String())
	}
	w = r.do(http.MethodGet, "/api/v1/agents", member, "")
	items := decode[AgentListResponse](t, w).Items
	if w.Code != http.StatusOK || len(items) != 1 || items[0].CUID != agentA {
		t.Fatalf("member should see exactly agent A: %d %s", w.Code, w.Body.String())
	}
	if w = r.do(http.MethodGet, "/api/v1/agents/"+agentA+"/members", member, ""); w.Code != http.StatusOK {
		t.Fatalf("member listing members should be 200, got %d %s", w.Code, w.Body.String())
	}

	// Member issues a key for A; the token is returned exactly once.
	w = r.do(http.MethodPost, "/api/v1/agents/"+agentA+"/keys", member, `{"name":"laptop","expires_in":"30d"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", w.Code, w.Body.String())
	}
	created := decode[OperatorAPIKey](t, w)
	if !auth.IsAgentKey(created.Token) || created.CreatedBy != me.UserID || created.ExpiresAt == nil || !created.Active || created.Name != "laptop" {
		t.Fatalf("unexpected created key: %+v", created)
	}
	w = r.do(http.MethodGet, "/api/v1/agents/"+agentA+"/keys", member, "")
	list := decode[APIKeyListResponse](t, w)
	if w.Code != http.StatusOK || len(list.Items) != 1 || list.Items[0].Token != "" || list.Items[0].KeyPrefix != created.KeyPrefix {
		t.Fatalf("listing should hide token: %d %s", w.Code, w.Body.String())
	}
	if w = r.do(http.MethodPost, "/api/v1/agents/"+agentB+"/keys", member, `{"name":"nope"}`); w.Code != http.StatusForbidden {
		t.Fatalf("key on agent B should be 403, got %d", w.Code)
	}
	if w = r.do(http.MethodPost, "/api/v1/agents/"+agentA+"/keys", member, `{"name":"bad","expires_in":"soon"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad expires_in should be 400, got %d", w.Code)
	}

	// The key authenticates the agent on a runtime route as agents.id.
	w = r.do(http.MethodGet, "/api/v1/agent/rules?source=amp&tool=Read", created.Token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("runtime with key: %d %s", w.Code, w.Body.String())
	}
	if got := decode[AgentRulesResponse](t, w).AgentID; got != agentA {
		t.Fatalf("expected runtime agent id %q, got %q", agentA, got)
	}
	if w = r.do(http.MethodGet, "/api/v1/agent/rules?source=amp&tool=Read", "atr_bogus", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("bogus key should be 401, got %d", w.Code)
	}
	if w = r.do(http.MethodGet, "/api/v1/servers", created.Token, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("agent key must not open the operator API, got %d %s", w.Code, w.Body.String())
	}

	// Revocation by the member takes effect immediately.
	if w = r.do(http.MethodDelete, "/api/v1/agents/"+agentB+"/keys/"+created.ID, admin, ""); w.Code != http.StatusNotFound {
		t.Fatalf("revoking via the wrong agent should be 404, got %d", w.Code)
	}
	if w = r.do(http.MethodDelete, "/api/v1/agents/"+agentA+"/keys/"+created.ID, member, ""); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	if w = r.do(http.MethodGet, "/api/v1/agent/rules?source=amp&tool=Read", created.Token, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key should be 401, got %d", w.Code)
	}
	w = r.do(http.MethodGet, "/api/v1/agents/"+agentA+"/keys", member, "")
	if list = decode[APIKeyListResponse](t, w); len(list.Items) != 1 || list.Items[0].Active || list.Items[0].RevokedBy != me.UserID {
		t.Fatalf("revoked key should remain listed inactive: %s", w.Body.String())
	}

	// Disabling the user cascades: their fresh key dies and they lose access.
	w = r.do(http.MethodPost, "/api/v1/agents/"+agentA+"/keys", member, `{"name":"second"}`)
	second := decode[OperatorAPIKey](t, w)
	if w.Code != http.StatusCreated {
		t.Fatalf("create second key: %d %s", w.Code, w.Body.String())
	}
	if w = r.do(http.MethodPatch, "/api/v1/users/"+me.UserID, member, `{"disabled":true}`); w.Code != http.StatusForbidden {
		t.Fatalf("member disabling users should be 403, got %d", w.Code)
	}
	w = r.do(http.MethodPatch, "/api/v1/users/"+me.UserID, admin, `{"disabled":true}`)
	if w.Code != http.StatusOK || !decode[OperatorUser](t, w).Disabled {
		t.Fatalf("disable user: %d %s", w.Code, w.Body.String())
	}
	if w = r.do(http.MethodGet, "/api/v1/agent/rules?source=amp&tool=Read", second.Token, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user's key should be 401, got %d", w.Code)
	}
	if w = r.do(http.MethodGet, "/api/v1/me", member, ""); w.Code != http.StatusForbidden {
		t.Fatalf("disabled user login should be 403, got %d %s", w.Code, w.Body.String())
	}
	w = r.do(http.MethodGet, "/api/v1/agents/"+agentA+"/members", admin, "")
	if members := decode[AgentMemberListResponse](t, w).Items; len(members) != 1 || members[0].UserID == me.UserID {
		t.Fatalf("disabled user should have been removed from agent members: %s", w.Body.String())
	}

	// Admin cannot disable themselves; admin can list users.
	w = r.do(http.MethodGet, "/api/v1/users", admin, "")
	users := decode[UserListResponse](t, w).Items
	if w.Code != http.StatusOK || len(users) != 2 {
		t.Fatalf("list users: %d %s", w.Code, w.Body.String())
	}
	var adminID string
	for _, u := range users {
		if u.Role == "admin" {
			adminID = u.ID
		}
	}
	if w = r.do(http.MethodPatch, "/api/v1/users/"+adminID, admin, `{"disabled":true}`); w.Code != http.StatusBadRequest {
		t.Fatalf("self-disable should be 400, got %d %s", w.Code, w.Body.String())
	}
}

func TestMemberCreatedAgentIsOwnedByCreator(t *testing.T) {
	r := newIdentityRig(t)
	member := r.token("member-2", false)
	w := r.do(http.MethodPost, "/api/v1/agents", member, `{"name":"Mine","enabled":true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("member create agent: %d %s", w.Code, w.Body.String())
	}
	id := decode[OperatorAgent](t, w).CUID
	me := decode[MeResponse](t, r.do(http.MethodGet, "/api/v1/me", member, ""))
	if len(me.AgentIDs) != 1 || me.AgentIDs[0] != id {
		t.Fatalf("creator should be a member of the new agent, got %+v", me)
	}
	if w = r.do(http.MethodPost, "/api/v1/agents/"+id+"/keys", member, `{"name":"k"}`); w.Code != http.StatusCreated {
		t.Fatalf("creator should be able to issue keys: %d %s", w.Code, w.Body.String())
	}
	// Editing/deleting stays admin-only in the open core.
	if w = r.do(http.MethodPatch, "/api/v1/agents/"+id, member, `{"name":"Renamed"}`); w.Code != http.StatusForbidden {
		t.Fatalf("member edit should be 403, got %d", w.Code)
	}
	if w = r.do(http.MethodDelete, "/api/v1/agents/"+id, member, ""); w.Code != http.StatusForbidden {
		t.Fatalf("member delete should be 403, got %d", w.Code)
	}
}

func TestNoAuthDeploymentCanStillIssueAndUseKeys(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "noauth.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	keys := store.NewAPIKeysRepo(db)
	agents := store.NewAgentsRepo(db)
	h := NewHandler(&stubService{}, stubServerService{}, nil, &stubRulesRepo{}, agents, nil, nil, nil, nil, nil)
	h.SetIdentityStores(store.NewUsersRepo(db), store.NewAgentMembersRepo(db), keys)
	h.SetAgentKeyResolver(NewAgentKeyResolver(keys, agents))
	handler := h.Routes()

	do := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	w := do(http.MethodPost, "/api/v1/agents", "", `{"name":"Solo","enabled":true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create agent: %d %s", w.Code, w.Body.String())
	}
	id := decode[OperatorAgent](t, w).CUID
	w = do(http.MethodPost, "/api/v1/agents/"+id+"/keys", "", `{"name":"solo"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create key without users: %d %s", w.Code, w.Body.String())
	}
	key := decode[OperatorAPIKey](t, w)
	if key.CreatedBy != "" {
		t.Fatalf("no-auth key should have no creator, got %q", key.CreatedBy)
	}
	w = do(http.MethodGet, "/api/v1/agent/rules?source=amp&tool=Read", key.Token, "")
	if w.Code != http.StatusOK || decode[AgentRulesResponse](t, w).AgentID != id {
		t.Fatalf("key should identify agent in no-auth mode: %d %s", w.Code, w.Body.String())
	}
	if w = do(http.MethodGet, "/api/v1/agent/rules?source=amp&tool=Read&agent_id=self-declared", "", ""); w.Code != http.StatusOK || decode[AgentRulesResponse](t, w).AgentID != "self-declared" {
		t.Fatalf("anonymous self-declared hint should still work: %d %s", w.Code, w.Body.String())
	}
	if w = do(http.MethodGet, "/api/v1/agent/rules?source=amp&tool=Read", "atr_bogus", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("bogus key should be 401 even in no-auth mode, got %d", w.Code)
	}
}

type stubUserInfo struct {
	profile auth.UserProfile
	err     error
	calls   int
}

func (s *stubUserInfo) FetchUserInfo(_ context.Context, _, _ string) (auth.UserProfile, error) {
	s.calls++
	return s.profile, s.err
}

func TestUserProvisionerEnrichesFromUserInfoOnce(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "prov.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	users := store.NewUsersRepo(db)
	info := &stubUserInfo{profile: auth.UserProfile{Email: "erin@example.com", Name: "Erin"}}
	prov := NewUserProvisioner(users).WithUserInfo(info)
	ctx := context.Background()

	// Auth0-style token: only a sub.
	id := auth.UserIdentity{Issuer: "https://tenant.auth0.example/", Subject: "google-oauth2|123", AccessToken: "tok"}
	p, err := prov.ProvisionUser(ctx, id)
	if err != nil || p.Email != "erin@example.com" || p.Name != "Erin" || p.UserID == "" {
		t.Fatalf("expected enriched principal, got %+v err=%v", p, err)
	}
	if info.calls != 1 {
		t.Fatalf("expected one userinfo call, got %d", info.calls)
	}
	// Next login with the same bare token: no further userinfo call, email kept.
	p, err = prov.ProvisionUser(ctx, id)
	if err != nil || p.Email != "erin@example.com" || info.calls != 1 {
		t.Fatalf("second login: %+v err=%v calls=%d", p, err, info.calls)
	}
	// A userinfo failure never blocks login.
	failing := NewUserProvisioner(users).WithUserInfo(&stubUserInfo{err: context.DeadlineExceeded})
	other := auth.UserIdentity{Issuer: "https://tenant.auth0.example/", Subject: "auth0|999", AccessToken: "tok"}
	if p, err := failing.ProvisionUser(ctx, other); err != nil || p.Subject != "auth0|999" {
		t.Fatalf("login should succeed despite userinfo failure: %+v err=%v", p, err)
	}
}
