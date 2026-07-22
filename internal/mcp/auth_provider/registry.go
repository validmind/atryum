package authprovider

import (
	"context"
	"fmt"

	"github.com/validmind/atryum/internal/mcp"
)

type Registry struct {
	providers []Provider
}

// NewRegistry returns providers in detection-priority order. Order is
// load-bearing: Detect() returns the first provider whose CanHandle is true,
// so static escape hatches (bearer, custom headers) must come before
// configured OAuth client setups, which in turn come before the catch-all
// discovery dispatcher.
func NewRegistry() *Registry {
	return &Registry{providers: []Provider{
		BearerTokenProvider{},
		CustomHeaderProvider{},
		OAuthClientSecretProvider{},
		OAuthPKCEProvider{},
		OAuthDynamicClientProvider{},
		OAuthMetadataProvider{},
	}}
}

func (r *Registry) Detect(ctx context.Context, upstream mcp.Upstream) (Provider, error) {
	for _, provider := range r.providers {
		if provider.CanHandle(upstream) {
			return provider, nil
		}
	}
	return nil, fmt.Errorf("no auth provider detected")
}

func (r *Registry) Get(id string) (Provider, error) {
	for _, provider := range r.providers {
		if provider.ID() == id {
			return provider, nil
		}
	}
	return nil, fmt.Errorf("unknown auth provider %q", id)
}

// Prepare runs the highest-priority provider's Prepare against the upstream.
// The discovery dispatcher (OAuthMetadataProvider) is what ultimately
// resolves OAuthProviderID for HTTP upstreams that aren't using a static
// escape hatch.
func Prepare(ctx context.Context, registry *Registry, upstream mcp.Upstream) (mcp.Upstream, error) {
	provider, err := registry.Detect(ctx, upstream)
	if err != nil {
		return upstream, err
	}
	return provider.Prepare(ctx, upstream)
}
