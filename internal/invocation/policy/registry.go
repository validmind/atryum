package policy

import (
	"context"
	"fmt"
	"sync"
)

// Registry holds the available policy providers and tracks which one is active.
// It implements Provider itself, delegating Evaluate to the active provider, so
// the Service can hold a single Provider interface value that always reflects
// runtime provider switches made through the operator API.
// Safe for concurrent use.
type Registry struct {
	mu        sync.RWMutex
	providers []Provider
	active    Provider
}

func NewRegistry(providers ...Provider) *Registry {
	return &Registry{providers: append([]Provider(nil), providers...)}
}

// SetActive selects the provider with the given ID as the active evaluator.
func (r *Registry) SetActive(id string) error {
	p, err := r.Get(id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.active = p
	r.mu.Unlock()
	return nil
}

// Active returns the currently active provider.
func (r *Registry) Active() Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.active
}

// Get returns the provider registered under id.
func (r *Registry) Get(id string) (Provider, error) {
	for _, p := range r.providers {
		if p.ID() == id {
			return p, nil
		}
	}
	return nil, fmt.Errorf("unknown policy provider %q", id)
}

// Providers returns a snapshot of all registered providers.
func (r *Registry) Providers() []Provider {
	out := make([]Provider, len(r.providers))
	copy(out, r.providers)
	return out
}

// ID and DisplayName satisfy the Provider interface so Registry can be passed
// directly as a Provider to the Service.
func (r *Registry) ID() string          { return "registry" }
func (r *Registry) DisplayName() string { return "Registry" }

// Evaluate delegates to the active provider. Falls back to DispositionHuman if
// no provider has been set, so the system fails closed rather than open.
func (r *Registry) Evaluate(ctx context.Context, call CallContext) (Decision, error) {
	r.mu.RLock()
	active := r.active
	r.mu.RUnlock()
	if active == nil {
		return Decision{Disposition: DispositionHuman, Reason: "no policy provider configured"}, nil
	}
	return active.Evaluate(ctx, call)
}
