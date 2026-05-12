package policy

import (
	"context"
	"sync"
	"time"
)

// TimedApproveProvider auto-approves all calls within a time window, then
// delegates to a configurable fallback provider once the window expires or
// has not been set. The window and fallback can be updated at runtime via
// SetWindow (e.g. from the admin API). Safe for concurrent use.
type TimedApproveProvider struct {
	mu        sync.RWMutex
	expiresAt *time.Time
	fallback  Provider
}

// NewTimedApproveProvider creates the provider with the given fallback.
// If window > 0 an initial approval window is set to time.Now().Add(window).
// fallback must not be nil.
func NewTimedApproveProvider(window time.Duration, fallback Provider) *TimedApproveProvider {
	p := &TimedApproveProvider{fallback: fallback}
	if window > 0 {
		t := time.Now().Add(window)
		p.expiresAt = &t
	}
	return p
}

func (p *TimedApproveProvider) ID() string          { return "timed_approve" }
func (p *TimedApproveProvider) DisplayName() string { return "Timed Approve" }

// SetWindow replaces the approval window. Calls before expiresAt are
// auto-approved; calls after are evaluated by fallback. Passing a nil fallback
// leaves the existing fallback unchanged.
func (p *TimedApproveProvider) SetWindow(expiresAt time.Time, fallback Provider) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := expiresAt
	p.expiresAt = &t
	if fallback != nil {
		p.fallback = fallback
	}
}

// WindowExpiresAt returns a copy of the current expiry, or nil if no window is set.
func (p *TimedApproveProvider) WindowExpiresAt() *time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.expiresAt == nil {
		return nil
	}
	t := *p.expiresAt
	return &t
}

// FallbackProvider returns the provider that will be used once the window expires.
func (p *TimedApproveProvider) FallbackProvider() Provider {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.fallback
}

func (p *TimedApproveProvider) Evaluate(ctx context.Context, call CallContext) (Decision, error) {
	p.mu.RLock()
	exp := p.expiresAt
	fallback := p.fallback
	p.mu.RUnlock()

	if exp != nil && time.Now().Before(*exp) {
		return Decision{Disposition: DispositionAuto, Reason: "within timed approval window", ExpiresAt: exp}, nil
	}
	return fallback.Evaluate(ctx, call)
}
