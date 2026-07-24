package managedagents

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/validmind/atryum/internal/invocation"
)

// InvocationGateway is the slice of invocation.Service the bridge reuses to run
// approval rules and record execution outcomes — exactly the external/hook
// path. The bridge never executes tools itself; Anthropic does.
type InvocationGateway interface {
	Submit(ctx context.Context, req invocation.ExternalSubmitRequest) (invocation.InvocationResponse, error)
	Get(ctx context.Context, invocationID string) (invocation.InvocationResponse, error)
	RecordExecution(ctx context.Context, invocationID string, update invocation.ExternalExecutionUpdate) (invocation.InvocationResponse, error)
}

// SessionStore persists which sessions are watched plus each one's cursor.
type SessionStore interface {
	Upsert(ctx context.Context, s SessionRegistration) error
	Get(ctx context.Context, sessionID string) (SessionRegistration, error)
	List(ctx context.Context) ([]SessionRegistration, error)
	Delete(ctx context.Context, sessionID string) error
	UpdateCursor(ctx context.Context, sessionID, lastEventID string) error
}

// BindingStore supplies Atryum Agent ↔ Claude Managed Agent links for automatic
// session discovery.
type BindingStore interface {
	List(ctx context.Context) ([]AgentBinding, error)
}

// Account pairs an Anthropic API client with its (defaulted) config.
type account struct {
	client AnthropicClient
	cfg    Config
}

// Account is a configured Anthropic account the bridge can watch sessions for.
type Account struct {
	Client AnthropicClient
	Config Config
}

// Service owns one Anthropic client per configured account, the session
// registry, and one watcher goroutine per registered session.
type Service struct {
	inv      InvocationGateway
	sessions SessionStore
	audit    InvocationAuditStore
	bindings BindingStore

	accounts       map[string]account
	defaultAccount string // the sole account's name, when exactly one is configured
	instanceName   string

	rootCtx context.Context
	cancel  context.CancelFunc

	mu       sync.Mutex
	watchers map[string]context.CancelFunc
	wg       sync.WaitGroup
}

const (
	DefaultInstanceName  = "atryum"
	metadataManagedKey   = "atryum_managed"
	metadataInstanceKey  = "atryum_instance"
	metadataWorkspaceKey = "atryum_workspace"
	metadataAgentCUIDKey = "atryum_agent_cuid"
	metadataBindingIDKey = "atryum_binding_id"
	metadataBoundAtKey   = "atryum_bound_at"
)

// AgentClaimConflictError reports an existing Atryum ownership marker on a
// Claude Managed Agent.
type AgentClaimConflictError struct {
	ClaudeAgentID string
	Instance      string
	AgentCUID     string
}

func (e *AgentClaimConflictError) Error() string {
	return fmt.Sprintf("Claude managed agent %s is already linked to Atryum instance %q agent %q", e.ClaudeAgentID, e.Instance, e.AgentCUID)
}

// NewService builds a bridge over one or more Anthropic accounts. Each account's
// config is normalized (name defaulted) on construction.
func NewService(inv InvocationGateway, sessions SessionStore, audit InvocationAuditStore, accounts []Account) (*Service, error) {
	s := &Service{
		inv:          inv,
		sessions:     sessions,
		audit:        audit,
		accounts:     make(map[string]account, len(accounts)),
		watchers:     make(map[string]context.CancelFunc),
		instanceName: DefaultInstanceName,
	}
	for _, a := range accounts {
		cfg := a.Config.withDefaults()
		if _, exists := s.accounts[cfg.Name]; exists {
			return nil, fmt.Errorf("duplicate managed-agent account name %q", cfg.Name)
		}
		s.accounts[cfg.Name] = account{client: a.Client, cfg: cfg}
	}
	if len(s.accounts) == 1 {
		for name := range s.accounts {
			s.defaultAccount = name
		}
	}
	return s, nil
}

// SetInstanceName sets the stable Atryum identity written to Claude agent
// metadata. Empty values fall back to DefaultInstanceName.
func (s *Service) SetInstanceName(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultInstanceName
	}
	s.instanceName = name
}

// Start resumes watching every previously-registered session. It is safe to
// call once at startup.
func (s *Service) Start(ctx context.Context) error {
	s.rootCtx, s.cancel = context.WithCancel(context.Background())
	regs, err := s.sessions.List(ctx)
	if err != nil {
		return fmt.Errorf("list managed-agent sessions: %w", err)
	}
	started := 0
	for _, reg := range regs {
		acct, ok := s.accounts[reg.Account]
		if !ok {
			slog.Warn("managed agents: skipping session for unknown account", "session_id", reg.SessionID, "account", reg.Account)
			continue
		}
		reg = s.rewindCursorIfParkedOnPendingAction(ctx, reg, acct.client)
		s.startWatcher(reg)
		started++
	}
	if s.bindings != nil {
		s.startDiscovery()
	}
	slog.Info("managed agents: started", "accounts", len(s.accounts), "watched_sessions", started)
	return nil
}

// SetBindings enables automatic discovery of Claude sessions for configured
// Atryum Agent ↔ Claude Managed Agent links. Call before Start.
func (s *Service) SetBindings(bindings BindingStore) {
	s.bindings = bindings
}

// Close stops all watchers and waits for them to drain.
func (s *Service) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	return nil
}

// RegisterSession persists a session and begins watching it immediately.
func (s *Service) RegisterSession(ctx context.Context, req RegisterSessionRequest) (SessionRegistration, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return SessionRegistration{}, fmt.Errorf("session_id is required")
	}
	accountName, err := s.resolveAccount(strings.TrimSpace(req.Account))
	if err != nil {
		return SessionRegistration{}, err
	}
	now := time.Now().UTC()
	reg := SessionRegistration{
		SessionID:   sessionID,
		Account:     accountName,
		AgentID:     strings.TrimSpace(req.AgentID),
		Description: strings.TrimSpace(req.Description),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.sessions.Upsert(ctx, reg); err != nil {
		return SessionRegistration{}, err
	}
	// Re-read to pick up the preserved cursor on re-registration.
	stored, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		stored = reg
	}
	s.startWatcher(stored)
	return stored, nil
}

// ListSessions returns every persisted Claude Managed Agents session watcher.
func (s *Service) ListSessions(ctx context.Context) ([]SessionRegistration, error) {
	return s.sessions.List(ctx)
}

// DeleteSession stops a watcher and removes its persisted registration.
func (s *Service) DeleteSession(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	if err := s.sessions.Delete(ctx, sessionID); err != nil {
		return err
	}
	s.mu.Lock()
	if cancel, ok := s.watchers[sessionID]; ok {
		cancel()
		delete(s.watchers, sessionID)
	}
	s.mu.Unlock()
	return nil
}

// ClearSessions stops and removes every persisted Claude Managed Agents session
// watcher. Linked agents will be rediscovered by the discovery loop.
func (s *Service) ClearSessions(ctx context.Context) (int, error) {
	sessions, err := s.sessions.List(ctx)
	if err != nil {
		return 0, err
	}
	cleared := 0
	for _, session := range sessions {
		if err := s.sessions.Delete(ctx, session.SessionID); err != nil {
			return cleared, err
		}
		s.mu.Lock()
		if cancel, ok := s.watchers[session.SessionID]; ok {
			cancel()
			delete(s.watchers, session.SessionID)
		}
		s.mu.Unlock()
		cleared++
	}
	return cleared, nil
}

func (s *Service) startDiscovery() {
	if s.rootCtx == nil {
		s.rootCtx, s.cancel = context.WithCancel(context.Background())
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.discoveryLoop(s.rootCtx)
	}()
}

func (s *Service) discoveryLoop(ctx context.Context) {
	// Discover immediately so the UI binding becomes active without waiting for
	// the first tick.
	s.discoverSessions(ctx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.discoverSessions(ctx)
		}
	}
}

func (s *Service) discoverSessions(ctx context.Context) {
	bindings, err := s.bindings.List(ctx)
	if err != nil {
		slog.Warn("managed agents: list bindings failed", "error", err)
		return
	}
	if len(bindings) == 0 {
		return
	}
	regs, err := s.sessions.List(ctx)
	if err != nil {
		slog.Warn("managed agents: list watched sessions failed", "error", err)
		return
	}
	known := make(map[string]bool, len(regs))
	for _, reg := range regs {
		known[reg.SessionID] = true
	}
	for _, binding := range bindings {
		if ctx.Err() != nil {
			return
		}
		claudeAgentID := strings.TrimSpace(binding.ClaudeAgentID)
		if claudeAgentID == "" {
			continue
		}
		accountName, err := s.resolveAccount(strings.TrimSpace(binding.Account))
		if err != nil {
			slog.Warn("managed agents: skipping binding with unknown account", "agent_cuid", binding.AgentCUID, "account", binding.Account, "error", err)
			continue
		}
		acct := s.accounts[accountName]
		sessions, err := acct.client.ListSessions(ctx, SessionListFilter{AgentID: claudeAgentID})
		if err != nil {
			slog.Warn("managed agents: discover sessions failed", "account", accountName, "claude_agent_id", claudeAgentID, "error", err)
			continue
		}
		for _, session := range sessions {
			if session.ID == "" || known[session.ID] {
				continue
			}
			description := strings.TrimSpace(session.Title)
			if description == "" {
				description = strings.TrimSpace(binding.ClaudeAgentName)
			}
			lastEventID, _, _ := s.discoveryTailCursor(ctx, acct.client, session.ID)
			reg := SessionRegistration{
				SessionID:   session.ID,
				Account:     accountName,
				AgentID:     claudeAgentID,
				Description: description,
				LastEventID: lastEventID,
				CreatedAt:   time.Now().UTC(),
				UpdatedAt:   time.Now().UTC(),
			}
			if err := s.sessions.Upsert(ctx, reg); err != nil {
				slog.Warn("managed agents: persist discovered session failed", "session_id", session.ID, "account", accountName, "claude_agent_id", claudeAgentID, "error", err)
				continue
			}
			stored, err := s.sessions.Get(ctx, session.ID)
			if err != nil {
				stored = reg
			}
			s.startWatcher(stored)
			known[session.ID] = true
			slog.Info("managed agents: discovered session", "session_id", session.ID, "account", accountName, "claude_agent_id", claudeAgentID)
		}
	}
}

func (s *Service) rewindCursorIfParkedOnPendingAction(ctx context.Context, reg SessionRegistration, client AnthropicClient) SessionRegistration {
	cursor, latestID, pending := s.discoveryTailCursor(ctx, client, reg.SessionID)
	if !pending || latestID == "" || reg.LastEventID != latestID {
		return reg
	}
	reg.LastEventID = cursor
	if err := s.sessions.UpdateCursor(ctx, reg.SessionID, cursor); err != nil {
		slog.Warn("managed agents: could not rewind pending-action cursor", "session_id", reg.SessionID, "error", err)
	}
	return reg
}

func (s *Service) discoveryTailCursor(ctx context.Context, client AnthropicClient, sessionID string) (cursor string, latestID string, pending bool) {
	events, err := client.ListEventsSince(ctx, sessionID, "")
	if err != nil {
		slog.Warn("managed agents: list discovery tail failed", "session_id", sessionID, "error", err)
		return "", "", false
	}
	if len(events) == 0 {
		return "", "", false
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].ID != "" {
			latestID = events[i].ID
			break
		}
	}
	latest := events[len(events)-1]
	blockingIDs, ok := requiresAction(latest)
	if !ok || len(blockingIDs) == 0 {
		return latestID, latestID, false
	}
	needed := make(map[string]bool, len(blockingIDs))
	for _, id := range blockingIDs {
		needed[id] = true
	}
	firstPendingIdx := -1
	for i, evt := range events {
		if needed[evt.ID] && isToolUse(evt.Type) {
			firstPendingIdx = i
			break
		}
	}
	if firstPendingIdx < 0 {
		slog.Warn("managed agents: requires_action references missing tool-use event in discovery tail; replaying session to recover", "session_id", sessionID)
		return "", latestID, true
	}
	for i := firstPendingIdx - 1; i >= 0; i-- {
		if events[i].ID != "" {
			return events[i].ID, latestID, true
		}
	}
	return "", latestID, true
}

// Accounts returns the configured Anthropic account names available for admin
// UI selection.
func (s *Service) Accounts() []AccountInfo {
	names := make([]string, 0, len(s.accounts))
	for name := range s.accounts {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]AccountInfo, 0, len(names))
	for _, name := range names {
		out = append(out, AccountInfo{Name: name, Workspace: s.accounts[name].cfg.Workspace})
	}
	return out
}

// ListAgents lists Claude Managed Agents in a configured account. The query is
// applied locally so callers are not coupled to Anthropic's filter support.
func (s *Service) ListAgents(ctx context.Context, req ListAgentsRequest) ([]AgentInfo, error) {
	accountName, err := s.resolveAccount(strings.TrimSpace(req.Account))
	if err != nil {
		return nil, err
	}
	agents, err := s.accounts[accountName].client.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(req.Query))
	if q == "" {
		return agents, nil
	}
	filtered := make([]AgentInfo, 0, len(agents))
	for _, a := range agents {
		if strings.Contains(strings.ToLower(a.ID), q) || strings.Contains(strings.ToLower(a.Name), q) || strings.Contains(strings.ToLower(a.Description), q) {
			filtered = append(filtered, a)
		}
	}
	return filtered, nil
}

// ClaimAgent writes Atryum ownership metadata to a Claude Managed Agent after
// checking whether another Atryum instance/agent already claimed it.
func (s *Service) ClaimAgent(ctx context.Context, req AgentClaimRequest) (AgentInfo, error) {
	accountName, err := s.resolveAccount(strings.TrimSpace(req.Account))
	if err != nil {
		return AgentInfo{}, err
	}
	agentID := strings.TrimSpace(req.ClaudeAgentID)
	if agentID == "" {
		return AgentInfo{}, fmt.Errorf("claude_agent_id is required")
	}
	agentCUID := strings.TrimSpace(req.AtryumAgentCUID)
	if agentCUID == "" {
		return AgentInfo{}, fmt.Errorf("atryum agent cuid is required")
	}
	agent, err := s.accounts[accountName].client.GetAgent(ctx, agentID)
	if err != nil {
		return AgentInfo{}, err
	}
	if agent.Metadata == nil {
		agent.Metadata = map[string]string{}
	}
	ownerInstance := agent.Metadata[metadataInstanceKey]
	ownerWorkspace := agent.Metadata[metadataWorkspaceKey]
	ownerAgentCUID := agent.Metadata[metadataAgentCUIDKey]
	bindingID := strings.TrimSpace(req.BindingID)
	if strings.EqualFold(agent.Metadata[metadataManagedKey], "true") && !req.Force {
		if ownerInstance != "" && ownerInstance != s.instanceName || ownerWorkspace != "" && ownerWorkspace != s.accounts[accountName].cfg.Workspace || ownerAgentCUID != "" && ownerAgentCUID != agentCUID {
			return AgentInfo{}, &AgentClaimConflictError{ClaudeAgentID: agentID, Instance: ownerInstance, AgentCUID: ownerAgentCUID}
		}
	}
	boundAt := time.Now().UTC().Format(time.RFC3339)
	if ownerInstance == s.instanceName && ownerWorkspace == s.accounts[accountName].cfg.Workspace && ownerAgentCUID == agentCUID && (bindingID == "" || agent.Metadata[metadataBindingIDKey] == bindingID) && agent.Metadata[metadataBoundAtKey] != "" {
		boundAt = agent.Metadata[metadataBoundAtKey]
	}
	patch := map[string]*string{
		metadataManagedKey:   strPtr("true"),
		metadataInstanceKey:  strPtr(s.instanceName),
		metadataWorkspaceKey: strPtr(s.accounts[accountName].cfg.Workspace),
		metadataAgentCUIDKey: strPtr(agentCUID),
		metadataBindingIDKey: strPtr(bindingID),
		metadataBoundAtKey:   strPtr(boundAt),
	}
	if metadataPatchIsNoop(agent.Metadata, patch) {
		return agent, nil
	}
	return s.accounts[accountName].client.UpdateAgentMetadata(ctx, agentID, agent.Version, patch)
}

// ReleaseAgent removes Atryum ownership metadata when the marker still belongs
// to this Atryum instance and binding. It is intentionally best-effort; stale
// metadata should not block local unlinking.
func (s *Service) ReleaseAgent(ctx context.Context, req AgentClaimRequest) error {
	accountName, err := s.resolveAccount(strings.TrimSpace(req.Account))
	if err != nil {
		return err
	}
	agentID := strings.TrimSpace(req.ClaudeAgentID)
	if agentID == "" {
		return nil
	}
	agent, err := s.accounts[accountName].client.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if agent.Metadata == nil || !strings.EqualFold(agent.Metadata[metadataManagedKey], "true") {
		return nil
	}
	if !req.Force {
		if agent.Metadata[metadataInstanceKey] != s.instanceName {
			return nil
		}
		if agent.Metadata[metadataWorkspaceKey] != "" && agent.Metadata[metadataWorkspaceKey] != s.accounts[accountName].cfg.Workspace {
			return nil
		}
		if req.AtryumAgentCUID != "" && agent.Metadata[metadataAgentCUIDKey] != req.AtryumAgentCUID {
			return nil
		}
		if req.BindingID != "" && agent.Metadata[metadataBindingIDKey] != req.BindingID {
			return nil
		}
	}
	patch := map[string]*string{
		metadataManagedKey:   nil,
		metadataInstanceKey:  nil,
		metadataWorkspaceKey: nil,
		metadataAgentCUIDKey: nil,
		metadataBindingIDKey: nil,
		metadataBoundAtKey:   nil,
	}
	_, err = s.accounts[accountName].client.UpdateAgentMetadata(ctx, agentID, agent.Version, patch)
	return err
}

func metadataPatchIsNoop(existing map[string]string, patch map[string]*string) bool {
	for key, value := range patch {
		if value == nil {
			if _, ok := existing[key]; ok {
				return false
			}
			continue
		}
		if existing[key] != *value {
			return false
		}
	}
	return true
}

// resolveAccount validates a requested account name (or picks the sole account
// when the name is empty and exactly one account is configured).
func (s *Service) resolveAccount(name string) (string, error) {
	if name == "" {
		if s.defaultAccount != "" {
			return s.defaultAccount, nil
		}
		return "", fmt.Errorf("account is required (multiple managed-agent accounts are configured)")
	}
	if _, ok := s.accounts[name]; !ok {
		return "", fmt.Errorf("unknown managed-agent account %q", name)
	}
	return name, nil
}

// startWatcher launches (or restarts) the watcher goroutine for a session.
func (s *Service) startWatcher(reg SessionRegistration) {
	if s.rootCtx == nil {
		// Start() hasn't run yet; this happens only if RegisterSession is
		// called before Start. Use a background root so the watcher still runs.
		s.rootCtx, s.cancel = context.WithCancel(context.Background())
	}
	acct, ok := s.accounts[reg.Account]
	if !ok {
		slog.Warn("managed agents: cannot watch session for unknown account", "session_id", reg.SessionID, "account", reg.Account)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel, ok := s.watchers[reg.SessionID]; ok {
		cancel() // replace any existing watcher
	}
	ctx, cancel := context.WithCancel(s.rootCtx)
	s.watchers[reg.SessionID] = cancel
	w := &watcher{svc: s, acct: acct, reg: reg, lastEventID: reg.LastEventID, pending: make(map[string]pendingCall)}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		w.run(ctx)
	}()
}
