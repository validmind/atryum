package invocation_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/config"
	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/invocation/policy"
	"github.com/validmind/atryum/internal/mcp"
	"github.com/validmind/atryum/internal/store"
)

// newProviderService builds a service whose resolver asks provider for
// credentials, with an auto-approve rule so calls execute at once.
func newProviderService(t *testing.T, upstreamURL string, provider mcp.CredentialProvider) *invocation.Service {
	t.Helper()
	db := newSQLiteTestDB(t)
	cfg := config.Config{
		Defaults:  config.DefaultsConfig{RequestTimeoutSeconds: 5},
		Upstreams: []config.UpstreamConfig{{Name: "demo", Mode: "http", BaseURL: upstreamURL, Enabled: true, TimeoutSeconds: 5}},
	}
	resolver := mcp.NewResolver(store.NewServerRepo(db), cfg).WithCredentialProvider(provider)
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	rule := invocation.ApprovalRule{ID: "rule_auto_approve", Action: invocation.RuleActionAutoApprove, ServerPatterns: []string{"*"}, ToolPatterns: []string{"*"}, Enabled: true}
	if err := store.NewRulesRepo(db).Create(context.Background(), store.Rule{ID: rule.ID, Action: string(rule.Action), ServerPatterns: rule.ServerPatterns, ToolPatterns: rule.ToolPatterns, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return invocation.NewService(
		store.NewInvocationRepo(db), store.NewEventRepo(db), resolver,
		mcp.NewHTTPClient(), policy.AlwaysDenyProvider{},
		5*time.Second, stubRulesStore{rules: []invocation.ApprovalRule{rule}}, nil, nil, nil,
	)
}

func TestExecutionAsksTheProviderAgain(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}})
	}))
	defer upstream.Close()

	var purposes []string
	var executedInvocation string
	svc := newProviderService(t, upstream.URL, func(ctx context.Context, u mcp.Upstream) (mcp.Upstream, error) {
		p := mcp.PurposeFromContext(ctx)
		purposes = append(purposes, string(p))
		u.AuthToken = "tok-" + string(p)
		if p == mcp.PurposeExecute {
			executedInvocation = mcp.InvocationIDFromContext(ctx)
		}
		return u, nil
	})

	ctx := auth.WithIdentity(context.Background(), auth.Identity{AgentID: "agent-007", Issuer: "https://idp.test"})
	resp, err := svc.Invoke(ctx, invocation.CreateInvocationRequest{Server: "demo", Tool: "demo_tool", Input: map[string]any{}})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if resp.Status != invocation.StatusSucceeded {
		t.Fatalf("status = %s", resp.Status)
	}
	if strings.Join(purposes, ",") != "call,execute" {
		t.Fatalf("provider purposes = %v, want call then execute", purposes)
	}
	if executedInvocation == "" || executedInvocation != resp.InvocationID {
		t.Fatalf("execute saw invocation %q, want %q", executedInvocation, resp.InvocationID)
	}
	for _, auth := range seen {
		if auth != "Bearer tok-execute" {
			t.Fatalf("upstream got %q; execution must use the credential fetched at execute", auth)
		}
	}
}

func TestExecutionFailsWhenTheProviderRefusesAtExecute(t *testing.T) {
	hits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	notConnected := errors.New("upstream_not_connected: Sam disconnected Demo")
	svc := newProviderService(t, upstream.URL, func(ctx context.Context, u mcp.Upstream) (mcp.Upstream, error) {
		if mcp.PurposeFromContext(ctx) == mcp.PurposeExecute {
			return mcp.Upstream{}, notConnected
		}
		return u, nil
	})
	resp, err := svc.Invoke(context.Background(), invocation.CreateInvocationRequest{Server: "demo", Tool: "demo_tool", Input: map[string]any{}})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if resp.Status != invocation.StatusFailed {
		t.Fatalf("status = %s, want failed", resp.Status)
	}
	if !strings.Contains(string(resp.Error), "upstream_not_connected:") {
		t.Fatalf("error = %s, want the provider's message", resp.Error)
	}
	if hits != 0 {
		t.Fatalf("upstream received %d requests; a refused execute must not contact it", hits)
	}
}

func TestUnavailableCallerIsShownTheEmbeddersToolsAndNotice(t *testing.T) {
	hits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(http.StatusInternalServerError) }))
	defer upstream.Close()

	placeholder := mcp.Tool{Name: "connect_account", Description: "Shortcut isn't connected for Bob. Connect it at https://aa.example-bank.com/connected-accounts?connect=shortcut."}
	svc := newProviderService(t, upstream.URL, func(ctx context.Context, u mcp.Upstream) (mcp.Upstream, error) {
		if mcp.PurposeFromContext(ctx) == mcp.PurposeCall {
			return u, nil
		}
		return mcp.Upstream{}, &mcp.Unavailable{Message: "not connected", Tools: []mcp.Tool{placeholder}, Instructions: "Connect Shortcut first."}
	})

	tools, err := svc.ListTools(context.Background(), "demo")
	if err != nil || len(tools) != 1 || tools[0].Name != "connect_account" {
		t.Fatalf("ListTools = %v, %v; want the embedder's placeholder and no error", tools, err)
	}
	if got := svc.Notice(context.Background(), "demo"); got != "Connect Shortcut first." {
		t.Fatalf("Notice = %q", got)
	}
	if hits != 0 {
		t.Fatalf("the upstream was contacted %d times; a caller who can't use it must never reach it", hits)
	}

	// An Unavailable with no tools to show is still an error, as before.
	svc = newProviderService(t, upstream.URL, func(ctx context.Context, u mcp.Upstream) (mcp.Upstream, error) {
		return mcp.Upstream{}, &mcp.Unavailable{Message: "not connected"}
	})
	if _, err := svc.ListTools(context.Background(), "demo"); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("ListTools with nothing to show: %v", err)
	}
	if got := svc.Notice(context.Background(), "demo"); got != "" {
		t.Fatalf("Notice with no instructions = %q", got)
	}
}

// A stuck report names the server and contacts nothing, so it needs no
// credential: a caller the provider refuses (no account yet) can still say
// they're stuck on that server.
func TestStuckReportAsksNoCredential(t *testing.T) {
	asked := 0
	svc := newProviderService(t, "http://127.0.0.1:1", func(ctx context.Context, u mcp.Upstream) (mcp.Upstream, error) {
		asked++
		return mcp.Upstream{}, &mcp.Unavailable{Message: "connect your account first"}
	})
	resp, err := svc.ReportStuck(context.Background(), "demo", map[string]any{"task": "pay INV-1", "boundary": "needs a person"})
	if err != nil {
		t.Fatalf("ReportStuck: %v", err)
	}
	if resp.Status != invocation.StatusReported || asked != 0 {
		t.Fatalf("report status %q, provider asked %d times; want reported, 0", resp.Status, asked)
	}
	if _, err := svc.ReportStuck(context.Background(), "nope", map[string]any{"task": "x", "boundary": "y"}); !errors.Is(err, invocation.ErrInvalidStuckReport) {
		t.Fatalf("unknown server: %v", err)
	}
}

// countingServers counts the reads a resolver makes of its server store.
type countingServers struct {
	mcp.ServerStore
	reads int
}

func (c *countingServers) GetServer(ctx context.Context, name string) (mcp.Upstream, error) {
	c.reads++
	return c.ServerStore.GetServer(ctx, name)
}

// Without a credential provider there is no notice to give, and asking
// would read the server and its stored credential on every initialize.
func TestNoticeWithoutProviderReadsNothing(t *testing.T) {
	db := newSQLiteTestDB(t)
	servers := &countingServers{ServerStore: store.NewServerRepo(db)}
	cfg := config.Config{Upstreams: []config.UpstreamConfig{{Name: "demo", Mode: "http", BaseURL: "http://127.0.0.1:1", Enabled: true, TimeoutSeconds: 5}}}
	resolver := mcp.NewResolver(servers, cfg)
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc := invocation.NewService(store.NewInvocationRepo(db), store.NewEventRepo(db), resolver, mcp.NewHTTPClient(), policy.AlwaysDenyProvider{},
		5*time.Second, stubRulesStore{}, nil, nil, nil)
	servers.reads = 0
	if n := svc.Notice(context.Background(), "demo"); n != "" {
		t.Fatalf("notice %q without a provider", n)
	}
	if servers.reads != 0 {
		t.Fatalf("Notice read the store %d times without a provider", servers.reads)
	}
	withProvider := mcp.NewResolver(servers, cfg).WithCredentialProvider(func(ctx context.Context, u mcp.Upstream) (mcp.Upstream, error) {
		return mcp.Upstream{}, &mcp.Unavailable{Instructions: "Connect your account."}
	})
	svc = invocation.NewService(store.NewInvocationRepo(db), store.NewEventRepo(db), withProvider, mcp.NewHTTPClient(), policy.AlwaysDenyProvider{},
		5*time.Second, stubRulesStore{}, nil, nil, nil)
	if n := svc.Notice(context.Background(), "demo"); n != "Connect your account." {
		t.Fatalf("notice with a provider = %q", n)
	}
}
