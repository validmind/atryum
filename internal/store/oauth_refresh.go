package store

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"atryum/internal/mcp"
)

const defaultOAuthRefreshSkew = 2 * time.Minute

type OAuthTokenRefresher interface {
	RefreshOAuthToken(ctx context.Context, upstream mcp.Upstream, refreshToken string) (mcp.OAuthToken, error)
}

type RefreshingOAuthCredentialStore struct {
	repo      *OAuthRepo
	refresher OAuthTokenRefresher
	skew      time.Duration

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewRefreshingOAuthCredentialStore(repo *OAuthRepo, refresher OAuthTokenRefresher) *RefreshingOAuthCredentialStore {
	return NewRefreshingOAuthCredentialStoreWithSkew(repo, refresher, defaultOAuthRefreshSkew)
}

func NewRefreshingOAuthCredentialStoreWithSkew(repo *OAuthRepo, refresher OAuthTokenRefresher, skew time.Duration) *RefreshingOAuthCredentialStore {
	if skew < 0 {
		skew = 0
	}
	return &RefreshingOAuthCredentialStore{
		repo:      repo,
		refresher: refresher,
		skew:      skew,
		locks:     make(map[string]*sync.Mutex),
	}
}

func (s *RefreshingOAuthCredentialStore) GetCredential(ctx context.Context, upstream mcp.Upstream) (mcp.AccessTokenView, error) {
	if s == nil || s.repo == nil {
		return mcp.AccessTokenView{}, nil
	}
	cred, err := s.repo.GetCredential(ctx, upstream.Name)
	if err != nil {
		return mcp.AccessTokenView{}, err
	}
	if !s.shouldRefresh(cred) {
		return mcp.AccessTokenView{AccessToken: cred.AccessToken}, nil
	}
	if s.refresher == nil || strings.TrimSpace(cred.RefreshToken) == "" || !canRefresh(upstream) {
		return mcp.AccessTokenView{AccessToken: cred.AccessToken}, nil
	}

	lock := s.lockFor(upstream.Name)
	lock.Lock()
	defer lock.Unlock()

	// Another goroutine may have refreshed while we waited.
	cred, err = s.repo.GetCredential(ctx, upstream.Name)
	if err != nil {
		return mcp.AccessTokenView{}, err
	}
	if !s.shouldRefresh(cred) {
		return mcp.AccessTokenView{AccessToken: cred.AccessToken}, nil
	}

	token, err := s.refresher.RefreshOAuthToken(ctx, upstream, cred.RefreshToken)
	if err != nil {
		log.Printf("[mcp-auth] oauth token refresh failed server=%s err=%v", upstream.Name, err)
		return mcp.AccessTokenView{AccessToken: cred.AccessToken}, nil
	}
	if strings.TrimSpace(token.RefreshToken) == "" {
		token.RefreshToken = cred.RefreshToken
	}
	if strings.TrimSpace(token.TokenType) == "" {
		token.TokenType = cred.TokenType
	}
	if strings.TrimSpace(token.Scope) == "" {
		token.Scope = cred.Scope
	}
	updated := OAuthCredential{
		ServerName:   cred.ServerName,
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    token.TokenType,
		Scope:        token.Scope,
		ExpiresAt:    token.ExpiresAt,
		CreatedAt:    cred.CreatedAt,
	}
	if err := s.repo.UpsertCredential(ctx, updated); err != nil {
		return mcp.AccessTokenView{}, err
	}
	return mcp.AccessTokenView{AccessToken: token.AccessToken}, nil
}

func (s *RefreshingOAuthCredentialStore) shouldRefresh(cred OAuthCredential) bool {
	if strings.TrimSpace(cred.AccessToken) == "" || cred.ExpiresAt == nil {
		return false
	}
	return !cred.ExpiresAt.After(time.Now().UTC().Add(s.skew))
}

func (s *RefreshingOAuthCredentialStore) lockFor(serverName string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock := s.locks[serverName]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[serverName] = lock
	}
	return lock
}

func canRefresh(upstream mcp.Upstream) bool {
	return strings.TrimSpace(upstream.OAuthTokenURL) != "" && strings.TrimSpace(upstream.OAuthClientID) != ""
}
