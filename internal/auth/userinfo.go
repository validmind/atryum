package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// UserProfile is what the userinfo fallback can learn about a user.
type UserProfile struct {
	Email string
	Name  string
}

// UserInfoClient fills in email/name for users whose access token omits them
// by calling the issuer's OIDC userinfo endpoint (discovered once per issuer
// from /.well-known/openid-configuration and cached). It is IdP-agnostic:
// Auth0, Okta, Keycloak and most OIDC providers accept the same access token
// they issued for an API audience. Providers that do not (Entra) can disable
// it per [[auth]] block with userinfo = false.
type UserInfoClient struct {
	v      *Validator
	client *http.Client

	mu        sync.Mutex
	endpoints map[string]string // issuer -> userinfo_endpoint ("" = none)
}

// NewUserInfoClient returns a client bound to the validator's issuer configs.
func NewUserInfoClient(v *Validator, client *http.Client) *UserInfoClient {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &UserInfoClient{v: v, client: client, endpoints: map[string]string{}}
}

// ErrUserInfoDisabled is returned when the issuer's config has userinfo = false.
var ErrUserInfoDisabled = errors.New("userinfo lookup disabled for issuer")

// FetchUserInfo calls the issuer's userinfo endpoint with token and maps the
// response through the issuer's email/name claim lists.
func (u *UserInfoClient) FetchUserInfo(ctx context.Context, issuer, token string) (UserProfile, error) {
	cfg, ok := u.v.ConfigForIssuer(issuer)
	if !ok {
		return UserProfile{}, fmt.Errorf("userinfo: unknown issuer %q", issuer)
	}
	if !cfg.UserInfoEnabled() {
		return UserProfile{}, ErrUserInfoDisabled
	}
	endpoint, err := u.endpointFor(ctx, cfg.Issuer)
	if err != nil {
		return UserProfile{}, err
	}
	if endpoint == "" {
		return UserProfile{}, fmt.Errorf("userinfo: issuer %s advertises no userinfo_endpoint", cfg.Issuer)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return UserProfile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Accept", "application/json")
	resp, err := u.client.Do(req)
	if err != nil {
		return UserProfile{}, fmt.Errorf("userinfo: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return UserProfile{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return UserProfile{}, fmt.Errorf("userinfo: %s returned %d", endpoint, resp.StatusCode)
	}
	var claims map[string]any
	if err := json.Unmarshal(body, &claims); err != nil {
		return UserProfile{}, fmt.Errorf("userinfo: decode: %w", err)
	}
	return UserProfile{
		Email: emailFromClaims(claims, cfg.EmailClaims),
		Name:  nameFromClaims(claims, cfg.NameClaims),
	}, nil
}

func (u *UserInfoClient) endpointFor(ctx context.Context, issuer string) (string, error) {
	u.mu.Lock()
	endpoint, cached := u.endpoints[issuer]
	u.mu.Unlock()
	if cached {
		return endpoint, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return "", err
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("userinfo: discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo: discovery for %s returned %d", issuer, resp.StatusCode)
	}
	var disc struct {
		UserInfoEndpoint string `json:"userinfo_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&disc); err != nil {
		return "", fmt.Errorf("userinfo: discovery decode: %w", err)
	}
	u.mu.Lock()
	u.endpoints[issuer] = disc.UserInfoEndpoint
	u.mu.Unlock()
	return disc.UserInfoEndpoint, nil
}
