package authprovider

import (
	"context"
	"fmt"

	"atryum/internal/mcp"
)

type Registry struct {
	providers []Provider
}

func NewRegistry() *Registry {
	return &Registry{providers: []Provider{
		BearerTokenProvider{},
		CustomHeaderProvider{},
		OAuthClientSecretProvider{},
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

func Prepare(ctx context.Context, registry *Registry, upstream mcp.Upstream) (mcp.Upstream, error) {
	provider, err := registry.Detect(ctx, upstream)
	if err != nil {
		return upstream, err
	}
	return provider.Prepare(ctx, upstream)
}
