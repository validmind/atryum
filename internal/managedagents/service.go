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

// Service owns the Anthropic client, the session registry, and one watcher
// goroutine per registered session.
type Service struct {
	client   AnthropicClient
	inv      InvocationGateway
	sessions SessionStore
	audit    InvocationAuditStore
	cfg      Config

	rootCtx context.Context
	cancel  context.CancelFunc

	mu       sync.Mutex
	watchers map[string]context.CancelFunc
	wg       sync.WaitGroup
}

func NewService(client AnthropicClient, inv InvocationGateway, sessions SessionStore, audit InvocationAuditStore, cfg Config) *Service {
	return &Service{
		client:   client,
		inv:      inv,
		sessions: sessions,
		audit:    audit,
		cfg:      cfg.withDefaults(),
		watchers: make(map[string]context.CancelFunc),
	}
}

// Start resumes watching every previously-registered session. It is safe to
// call once at startup.
func (s *Service) Start(ctx context.Context) error {
	s.rootCtx, s.cancel = context.WithCancel(context.Background())
	regs, err := s.sessions.List(ctx)
	if err != nil {
		return fmt.Errorf("list managed-agent sessions: %w", err)
	}
	for _, reg := range regs {
		s.startWatcher(reg)
	}
	slog.Info("managed agents: started", "watched_sessions", len(regs))
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
	now := time.Now().UTC()
	reg := SessionRegistration{
		SessionID:   sessionID,
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

// startWatcher launches (or restarts) the watcher goroutine for a session.
func (s *Service) startWatcher(reg SessionRegistration) {
	if s.rootCtx == nil {
		// Start() hasn't run yet; this happens only if RegisterSession is
		// called before Start. Use a background root so the watcher still runs.
		s.rootCtx, s.cancel = context.WithCancel(context.Background())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel, ok := s.watchers[reg.SessionID]; ok {
		cancel() // replace any existing watcher
	}
	ctx, cancel := context.WithCancel(s.rootCtx)
	s.watchers[reg.SessionID] = cancel
	w := &watcher{svc: s, reg: reg, lastEventID: reg.LastEventID, pending: make(map[string]pendingCall)}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		w.run(ctx)
	}()
}
