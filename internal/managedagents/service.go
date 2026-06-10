package managedagents

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"atryum/internal/invocation"
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
	UpdateCursor(ctx context.Context, sessionID, lastEventID string) error
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

	accounts       map[string]account
	defaultAccount string // the sole account's name, when exactly one is configured

	rootCtx context.Context
	cancel  context.CancelFunc

	mu       sync.Mutex
	watchers map[string]context.CancelFunc
	wg       sync.WaitGroup
}

// NewService builds a bridge over one or more Anthropic accounts. Each account's
// config is normalized (name defaulted) on construction.
func NewService(inv InvocationGateway, sessions SessionStore, audit InvocationAuditStore, accounts []Account) (*Service, error) {
	s := &Service{
		inv:      inv,
		sessions: sessions,
		audit:    audit,
		accounts: make(map[string]account, len(accounts)),
		watchers: make(map[string]context.CancelFunc),
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
		if _, ok := s.accounts[reg.Account]; !ok {
			slog.Warn("managed agents: skipping session for unknown account", "session_id", reg.SessionID, "account", reg.Account)
			continue
		}
		s.startWatcher(reg)
		started++
	}
	slog.Info("managed agents: started", "accounts", len(s.accounts), "watched_sessions", started)
	return nil
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
