package atryum

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/validmind/atryum/internal/api"
)

// ─── Credential store ────────────────────────────────────────────────────────

// credentialsFile is where `atryum login` keeps IdP tokens, keyed by Atryum
// server URL so one machine can talk to several deployments.
const credentialsFile = "credentials.json"

// serverCredentials is one logged-in session against one Atryum server.
type serverCredentials struct {
	ProviderID    string    `json:"provider_id"`
	Issuer        string    `json:"issuer"`
	ClientID      string    `json:"client_id"`
	TokenEndpoint string    `json:"token_endpoint"`
	Audience      string    `json:"audience,omitempty"`
	Scopes        string    `json:"scopes,omitempty"`
	AccessToken   string    `json:"access_token"`
	RefreshToken  string    `json:"refresh_token,omitempty"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func (c serverCredentials) expired(now time.Time) bool {
	return c.AccessToken == "" || !now.Before(c.ExpiresAt)
}

type credentialsStore struct {
	Servers map[string]serverCredentials `json:"servers"`
}

// atryumHomeDir returns ~/.atryum (created on demand), honouring ATRYUM_HOME
// for tests and unusual setups.
func atryumHomeDir() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("ATRYUM_HOME")); custom != "" {
		return custom, os.MkdirAll(custom, 0o700)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".atryum")
	return dir, os.MkdirAll(dir, 0o700)
}

func loadCredentials() (credentialsStore, string, error) {
	dir, err := atryumHomeDir()
	if err != nil {
		return credentialsStore{}, "", err
	}
	path := filepath.Join(dir, credentialsFile)
	store := credentialsStore{Servers: map[string]serverCredentials{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, path, nil
		}
		return store, path, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return store, path, nil
	}
	if err := json.Unmarshal(raw, &store); err != nil {
		return store, path, fmt.Errorf("parse %s: %w", path, err)
	}
	if store.Servers == nil {
		store.Servers = map[string]serverCredentials{}
	}
	return store, path, nil
}

func saveCredentials(store credentialsStore) error {
	dir, err := atryumHomeDir()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return writeSecretFile(filepath.Join(dir, credentialsFile), raw)
}

// writeSecretFile writes data with 0600 permissions, replacing any existing
// file atomically so a crash never leaves a half-written secret.
func writeSecretFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func normalizeServerURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// ─── OIDC device flow (RFC 8628) ─────────────────────────────────────────────

type oidcDiscovery struct {
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
}

type deviceAuthResponse struct {
	Error                   string `json:"error"`
	ErrorDescription        string `json:"error_description"`
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	TokenType        string `json:"token_type"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// tokenExpirySkew is subtracted from expires_in so we refresh a little early.
const tokenExpirySkew = 30 * time.Second

// deviceFlowMinInterval floors the poll interval; IdPs that omit `interval`
// get deviceFlowDefaultInterval (the RFC default of five seconds). Both are
// variables so tests can poll quickly.
var (
	deviceFlowMinInterval     = time.Second
	deviceFlowDefaultInterval = 5 * time.Second
)

var errLoginTimeout = errors.New("login timed out before the device code was approved")

func discoverOIDC(ctx context.Context, client *http.Client, issuer string) (oidcDiscovery, error) {
	endpoint := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return oidcDiscovery{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return oidcDiscovery{}, fmt.Errorf("fetch OIDC discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return oidcDiscovery{}, fmt.Errorf("OIDC discovery returned %d from %s", resp.StatusCode, endpoint)
	}
	var disc oidcDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&disc); err != nil {
		return oidcDiscovery{}, fmt.Errorf("decode OIDC discovery: %w", err)
	}
	if disc.TokenEndpoint == "" {
		return oidcDiscovery{}, errors.New("OIDC discovery has no token_endpoint")
	}
	if disc.DeviceAuthorizationEndpoint == "" {
		return oidcDiscovery{}, fmt.Errorf("identity provider %s does not advertise a device_authorization_endpoint; it must support the OAuth 2.0 device authorization grant for `atryum login`", issuer)
	}
	return disc, nil
}

// deviceFlowLogin runs the device authorization grant against provider and
// returns stored credentials. It prints the verification URL and code to out
// and blocks until the user approves, the code expires, or ctx is done.
func deviceFlowLogin(ctx context.Context, client *http.Client, out io.Writer, provider api.AuthProvider) (serverCredentials, error) {
	disc, err := discoverOIDC(ctx, client, provider.Issuer)
	if err != nil {
		return serverCredentials{}, err
	}

	clientID := firstNonEmpty(provider.CLIClientID, provider.ClientID)
	form := url.Values{"client_id": {clientID}}
	if scopes := strings.TrimSpace(provider.Scopes); scopes != "" {
		form.Set("scope", scopes)
	}
	if provider.Audience != "" {
		form.Set("audience", provider.Audience)
	}
	var dev deviceAuthResponse
	if err := postForm(ctx, client, disc.DeviceAuthorizationEndpoint, form, &dev); err != nil {
		return serverCredentials{}, fmt.Errorf("device authorization: %w", err)
	}
	if dev.Error != "" || dev.DeviceCode == "" {
		msg := strings.TrimSpace(dev.Error + " " + dev.ErrorDescription)
		if msg == "" {
			msg = "response had no device_code"
		}
		return serverCredentials{}, fmt.Errorf("device authorization for client %q at %s failed: %s\n\nThe identity provider must allow the OAuth 2.0 device authorization grant for this client. On Auth0 that requires a Native application with the \"Device Code\" grant enabled; point [[auth]] cli_client_id at it.", clientID, provider.Issuer, msg)
	}

	fmt.Fprintf(out, "\nTo sign in, open this URL in a browser:\n\n    %s\n\n", firstNonEmpty(dev.VerificationURIComplete, dev.VerificationURI))
	if dev.VerificationURIComplete == "" || !strings.Contains(dev.VerificationURIComplete, dev.UserCode) {
		fmt.Fprintf(out, "and enter the code: %s\n\n", dev.UserCode)
	} else {
		fmt.Fprintf(out, "(code: %s)\n\n", dev.UserCode)
	}
	fmt.Fprintln(out, "Waiting for approval...")

	interval := time.Duration(dev.Interval) * time.Second
	if interval <= 0 {
		interval = deviceFlowDefaultInterval
	}
	if interval < deviceFlowMinInterval {
		interval = deviceFlowMinInterval
	}
	deadline := time.Now().Add(time.Duration(dev.ExpiresIn) * time.Second)
	if dev.ExpiresIn <= 0 {
		deadline = time.Now().Add(10 * time.Minute)
	}

	for {
		select {
		case <-ctx.Done():
			return serverCredentials{}, ctx.Err()
		case <-time.After(interval):
		}
		if time.Now().After(deadline) {
			return serverCredentials{}, errLoginTimeout
		}
		poll := url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {dev.DeviceCode},
			"client_id":   {clientID},
		}
		var tok tokenResponse
		if err := postForm(ctx, client, disc.TokenEndpoint, poll, &tok); err != nil {
			return serverCredentials{}, fmt.Errorf("token poll: %w", err)
		}
		switch tok.Error {
		case "":
			if tok.AccessToken == "" {
				return serverCredentials{}, errors.New("token response had no access_token")
			}
			return credentialsFromToken(provider, disc.TokenEndpoint, tok), nil
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "expired_token":
			return serverCredentials{}, errLoginTimeout
		case "access_denied":
			return serverCredentials{}, errors.New("login was denied")
		default:
			return serverCredentials{}, fmt.Errorf("token endpoint error %q: %s", tok.Error, tok.ErrorDescription)
		}
	}
}

func credentialsFromToken(provider api.AuthProvider, tokenEndpoint string, tok tokenResponse) serverCredentials {
	ttl := time.Duration(tok.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	ttl -= tokenExpirySkew
	if ttl < 0 {
		ttl = 0
	}
	return serverCredentials{
		ProviderID:    provider.ID,
		Issuer:        provider.Issuer,
		ClientID:      firstNonEmpty(provider.CLIClientID, provider.ClientID),
		TokenEndpoint: tokenEndpoint,
		Audience:      provider.Audience,
		Scopes:        provider.Scopes,
		AccessToken:   tok.AccessToken,
		RefreshToken:  tok.RefreshToken,
		ExpiresAt:     time.Now().Add(ttl),
	}
}

// refreshCredentials exchanges the refresh token for a new access token.
func refreshCredentials(ctx context.Context, client *http.Client, creds serverCredentials) (serverCredentials, error) {
	if creds.RefreshToken == "" || creds.TokenEndpoint == "" {
		return creds, errors.New("no refresh token; run `atryum login`")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {creds.RefreshToken},
		"client_id":     {creds.ClientID},
	}
	var tok tokenResponse
	if err := postForm(ctx, client, creds.TokenEndpoint, form, &tok); err != nil {
		return creds, fmt.Errorf("refresh token: %w", err)
	}
	if tok.Error != "" {
		return creds, fmt.Errorf("refresh token rejected (%s); run `atryum login`", tok.Error)
	}
	provider := api.AuthProvider{ID: creds.ProviderID, Issuer: creds.Issuer, ClientID: creds.ClientID, Audience: creds.Audience, Scopes: creds.Scopes}
	next := credentialsFromToken(provider, creds.TokenEndpoint, tok)
	if next.RefreshToken == "" {
		next.RefreshToken = creds.RefreshToken
	}
	return next, nil
}

// postForm posts a URL-encoded form and decodes the JSON body regardless of
// status code (OAuth error responses are 400 with a JSON body).
func postForm(ctx context.Context, client *http.Client, endpoint string, form url.Values, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return fmt.Errorf("%s returned %d with an empty body", endpoint, resp.StatusCode)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("%s returned %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
