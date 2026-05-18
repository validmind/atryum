package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"atryum/internal/store"
)

type stubClaudeAgentsRepo struct {
	agents   map[string]store.ClaudeAgent
	sessions map[string][]store.ClaudeAgentSession
}

func newStubClaudeAgentsRepo() *stubClaudeAgentsRepo {
	return &stubClaudeAgentsRepo{
		agents:   map[string]store.ClaudeAgent{},
		sessions: map[string][]store.ClaudeAgentSession{},
	}
}

func (s *stubClaudeAgentsRepo) CreateAgent(_ context.Context, agent store.ClaudeAgent) error {
	if _, dup := s.agents[agent.ID]; dup {
		return errors.New("duplicate")
	}
	s.agents[agent.ID] = agent
	return nil
}

func (s *stubClaudeAgentsRepo) GetAgent(_ context.Context, id string) (store.ClaudeAgent, error) {
	a, ok := s.agents[id]
	if !ok {
		return store.ClaudeAgent{}, errNotFound
	}
	return a, nil
}

func (s *stubClaudeAgentsRepo) GetAgentByName(_ context.Context, name string) (store.ClaudeAgent, error) {
	for _, a := range s.agents {
		if a.Name == name {
			return a, nil
		}
	}
	return store.ClaudeAgent{}, errNotFound
}

func (s *stubClaudeAgentsRepo) ListAgents(context.Context) ([]store.ClaudeAgent, error) {
	out := make([]store.ClaudeAgent, 0, len(s.agents))
	for _, a := range s.agents {
		out = append(out, a)
	}
	return out, nil
}

func (s *stubClaudeAgentsRepo) UpdateAgent(_ context.Context, agent store.ClaudeAgent) error {
	if _, ok := s.agents[agent.ID]; !ok {
		return errNotFound
	}
	s.agents[agent.ID] = agent
	return nil
}

func (s *stubClaudeAgentsRepo) DeleteAgent(_ context.Context, id string) error {
	if _, ok := s.agents[id]; !ok {
		return errNotFound
	}
	delete(s.agents, id)
	delete(s.sessions, id)
	return nil
}

func (s *stubClaudeAgentsRepo) CreateSession(_ context.Context, session store.ClaudeAgentSession) error {
	s.sessions[session.AgentID] = append(s.sessions[session.AgentID], session)
	return nil
}

func (s *stubClaudeAgentsRepo) ListSessionsByAgent(_ context.Context, agentID string) ([]store.ClaudeAgentSession, error) {
	return s.sessions[agentID], nil
}

func (s *stubClaudeAgentsRepo) DeleteSession(_ context.Context, id string) error {
	for agentID, list := range s.sessions {
		for i, sess := range list {
			if sess.ID == id {
				s.sessions[agentID] = append(list[:i], list[i+1:]...)
				return nil
			}
		}
	}
	return errNotFound
}

var errNotFound = errors.New("not found")

func TestClaudeAgentsHandler_CreateAndList(t *testing.T) {
	repo := newStubClaudeAgentsRepo()
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, repo)

	body, _ := json.Marshal(AdminClaudeAgentInput{
		Name:        "PR Reviewer",
		AgentID:     "agent_abc",
		Description: "reviews PRs",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/claude-agents", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", w.Code, w.Body.String())
	}
	var created AdminClaudeAgent
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Name != "PR Reviewer" || created.AgentID != "agent_abc" || !created.Enabled {
		t.Fatalf("created = %+v", created)
	}

	// List should return one item.
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/claude-agents", nil)
	listW := httptest.NewRecorder()
	h.Routes().ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list: status = %d", listW.Code)
	}
	var listResp ClaudeAgentListResponse
	if err := json.Unmarshal(listW.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Items) != 1 {
		t.Fatalf("list len = %d", len(listResp.Items))
	}
}

func TestClaudeAgentsHandler_RegisterSession(t *testing.T) {
	repo := newStubClaudeAgentsRepo()
	repo.agents["cagent_a"] = store.ClaudeAgent{
		ID: "cagent_a", Name: "A", AgentID: "agent_a", Enabled: true,
	}
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, repo)

	body, _ := json.Marshal(AdminClaudeAgentSessionInput{SessionID: "sess_xyz"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/claude-agents/cagent_a/sessions", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var s AdminClaudeAgentSession
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.SessionID != "sess_xyz" {
		t.Fatalf("session = %+v", s)
	}
}

func TestClaudeAgentsHandler_NilRepoReturns501(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/claude-agents", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", w.Code)
	}
}

func TestClaudeAgentsHandler_ValidatesInput(t *testing.T) {
	repo := newStubClaudeAgentsRepo()
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, repo)

	body, _ := json.Marshal(AdminClaudeAgentInput{Name: "", AgentID: ""})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/claude-agents", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}
