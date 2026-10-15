package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/store"
	"github.com/validmind/atryum/pkg/authz"
)

// agentKeyStore is the slice of store.APIKeysRepo the key resolver needs.
type agentKeyStore interface {
	ResolveActive(ctx context.Context, keyHash string, now time.Time) (store.APIKey, error)
	TouchLastUsed(ctx context.Context, id string, at time.Time) error
}

// agentLookup is the slice of the agents store the key resolver needs.
type agentLookup interface {
	Get(ctx context.Context, id string) (store.AgentRecord, error)
}

// agentKeyTouchInterval bounds how often last_used_at is written per key so
// the runtime hot path stays read-mostly.
const agentKeyTouchInterval = time.Minute

// AgentKeyResolver bridges the api_keys store to auth.AgentKeyResolver. It
// hashes the presented token, resolves it (revocation, expiry and creator
// disablement are enforced by the store), checks the agent is still enabled,
// and stamps last_used_at at most once a minute.
type AgentKeyResolver struct {
	keys   agentKeyStore
	agents agentLookup
	now    func() time.Time
}

// NewAgentKeyResolver returns a resolver over the given stores.
func NewAgentKeyResolver(keys agentKeyStore, agents agentLookup) *AgentKeyResolver {
	return &AgentKeyResolver{keys: keys, agents: agents, now: time.Now}
}

// ResolveAgentKey implements auth.AgentKeyResolver.
func (r *AgentKeyResolver) ResolveAgentKey(ctx context.Context, token string) (auth.Identity, error) {
	now := r.now().UTC()
	key, err := r.keys.ResolveActive(ctx, auth.HashAgentKey(token), now)
	if err != nil {
		return auth.Identity{}, err
	}
	agent, err := r.agents.Get(ctx, key.AgentID)
	if err != nil {
		return auth.Identity{}, err
	}
	if !agent.Enabled {
		return auth.Identity{}, fmt.Errorf("agent %s is disabled", agent.ID)
	}
	if key.LastUsedAt == nil || now.Sub(*key.LastUsedAt) > agentKeyTouchInterval {
		// Best-effort; a failed touch must not fail the request.
		_ = r.keys.TouchLastUsed(context.WithoutCancel(ctx), key.ID, now)
	}
	return auth.Identity{
		AgentID: key.AgentID,
		Issuer:  auth.APIKeyIssuer,
		Subject: key.ID,
		Method:  auth.IdentityMethodAPIKey,
		KeyID:   key.ID,
		UserID:  key.CreatedBy,
	}, nil
}

// userLoginStore is the slice of store.UsersRepo the provisioner needs.
type userLoginStore interface {
	UpsertLogin(ctx context.Context, issuer, subject, email, name, role string) (store.User, error)
}

// UserInfoFetcher fills in email/name from the issuer's userinfo endpoint.
type UserInfoFetcher interface {
	FetchUserInfo(ctx context.Context, issuer, token string) (auth.UserProfile, error)
}

// UserProvisioner bridges the users store to auth.UserProvisioner: it upserts
// the user on every IdP login (just-in-time provisioning) and refuses
// disabled users. When the token carries no email and a UserInfoFetcher is
// installed, it enriches the row from the userinfo endpoint once; the store
// keeps known values when later logins present empty ones.
type UserProvisioner struct {
	users    userLoginStore
	userinfo UserInfoFetcher
}

// NewUserProvisioner returns a provisioner over the users store.
func NewUserProvisioner(users userLoginStore) *UserProvisioner {
	return &UserProvisioner{users: users}
}

// WithUserInfo enables userinfo enrichment for tokens that lack email/name.
func (p *UserProvisioner) WithUserInfo(f UserInfoFetcher) *UserProvisioner {
	p.userinfo = f
	return p
}

// ProvisionUser implements auth.UserProvisioner.
func (p *UserProvisioner) ProvisionUser(ctx context.Context, id auth.UserIdentity) (authz.Principal, error) {
	role := store.UserRoleMember
	if id.Admin {
		role = store.UserRoleAdmin
	}
	u, err := p.users.UpsertLogin(ctx, id.Issuer, id.Subject, id.Email, id.Name, role)
	if err != nil {
		return authz.Principal{}, err
	}
	if u.Email == "" && p.userinfo != nil && id.AccessToken != "" {
		// Best effort: a userinfo failure must never block a valid login.
		if profile, err := p.userinfo.FetchUserInfo(ctx, id.Issuer, id.AccessToken); err == nil && (profile.Email != "" || profile.Name != "") {
			if enriched, err := p.users.UpsertLogin(ctx, id.Issuer, id.Subject, profile.Email, profile.Name, role); err == nil {
				u = enriched
			}
		} else if err != nil && !errors.Is(err, auth.ErrUserInfoDisabled) {
			slog.Debug("userinfo enrichment failed", "issuer", id.Issuer, "subject", id.Subject, "error", err)
		}
	}
	if u.Disabled() {
		return authz.Principal{}, auth.ErrUserDisabled
	}
	return authz.Principal{
		UserID:  u.ID,
		Issuer:  u.Issuer,
		Subject: u.Subject,
		Email:   u.Email,
		Name:    u.Name,
		Role:    authz.Role(u.Role),
		Method:  authz.MethodJWT,
	}, nil
}
