package policy

import (
	"context"
	"sync"
	"time"
)

// TimedApproveProvider auto-approves all calls within a time window, falling
// back to DispositionHuman once the window expires or has not been set.
// The window can be updated at runtime via SetWindow (e.g. from the admin API).
// Safe for concurrent use.
type TimedApproveProvider struct {
	mu        sync.RWMutex
	expiresAt *time.Time
}

// NewTimedApproveProvider creates the provider. If window > 0 the initial
// approval window is set to time.Now().Add(window).
func NewTimedApproveProvider(window time.Duration) *TimedApproveProvider {
	p := &TimedApproveProvider{}
	if window > 0 {
		t := time.Now().Add(window)
		p.expiresAt = &t
	}
	return p
}

func (p *TimedApproveProvider) ID() string          { return "timed_approve" }
func (p *TimedApproveProvider) DisplayName() string { return "Timed Approve" }

// SetWindow replaces the current approval window. Calls arriving before
// expiresAt are auto-approved; calls after revert to human approval.
func (p *TimedApproveProvider) SetWindow(expiresAt time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := expiresAt
	p.expiresAt = &t
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

func (p *TimedApproveProvider) Evaluate(_ context.Context, _ CallContext) (Decision, error) {
	p.mu.RLock()
	exp := p.expiresAt
	p.mu.RUnlock()

	if exp != nil && time.Now().Before(*exp) {
		return Decision{Disposition: DispositionAuto, Reason: "within timed approval window", ExpiresAt: exp}, nil
	}
	return Decision{Disposition: DispositionHuman, Reason: "timed approval window expired or not set"}, nil
}
