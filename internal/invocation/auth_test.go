package invocation_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/store"
)

// How the caller authenticated reaches the decider (ToolCall.Auth), the
// event hook (Change.Invocation.Auth) and the stored invocation row.
func TestAuthReachesDeciderHookAndRow(t *testing.T) {
	upstream, _ := fakeMCP(t)
	db := newSQLiteTestDB(t)
	svc := newHookedService(t, db, upstream.URL)
	var decided invocation.Auth
	svc.SetDecider(func(_ context.Context, c invocation.ToolCall) (invocation.Decision, error) {
		decided = c.Auth
		return invocation.Decision{Verdict: invocation.VerdictAllow}, nil
	})
	var hooked invocation.Auth
	svc.SetEventHook(func(_ context.Context, _ *sql.Tx, c invocation.Change) error {
		if c.Type == "invocation.created" {
			hooked = c.Invocation.Auth
		}
		return nil
	})
	repo := store.NewInvocationRepo(db)

	jwt := auth.Identity{AgentID: "agent-a", Issuer: "https://idp.bank", Subject: "agent-a", Method: auth.IdentityMethodJWT}
	key := auth.Identity{AgentID: "8b1f-agent", Issuer: auth.APIKeyIssuer, Method: auth.IdentityMethodAPIKey, KeyID: "key_42"}
	hint := auth.Identity{AgentID: "self-declared", Issuer: "atryum:no-auth", Subject: "self-declared"}
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		submit  bool
		agentID string // a submit body's agent_id
		want    invocation.Auth
	}{
		{"jwt on the proxy", auth.WithIdentity(context.Background(), jwt), false, "", invocation.Auth{Issuer: "https://idp.bank", Method: "jwt"}},
		{"api key on submit", auth.WithIdentity(context.Background(), key), true, "", invocation.Auth{Issuer: "atryum:api-key", Method: "api_key", KeyID: "key_42"}},
		{"no-auth hint", auth.WithIdentity(context.Background(), hint), false, "", invocation.Auth{Issuer: "atryum:no-auth", Method: "no_auth"}},
		{"declared agent_id on submit", context.Background(), true, "harness-1", invocation.Auth{Method: "no_auth"}},
		{"anonymous", context.Background(), false, "", invocation.Auth{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decided, hooked = invocation.Auth{Method: "unset"}, invocation.Auth{Method: "unset"}
			var resp invocation.InvocationResponse
			var err error
			if tc.submit {
				resp, err = svc.Submit(tc.ctx, invocation.ExternalSubmitRequest{Source: "harness", Tool: "Bash", Input: map[string]any{}, AgentID: tc.agentID})
			} else {
				resp, err = svc.Invoke(tc.ctx, invocation.CreateInvocationRequest{Server: "bank", Tool: "pay", Input: map[string]any{}})
			}
			if err != nil {
				t.Fatal(err)
			}
			row, err := repo.Get(context.Background(), resp.InvocationID)
			if err != nil {
				t.Fatal(err)
			}
			if decided != tc.want || hooked != tc.want || row.Auth != tc.want {
				t.Fatalf("decider %+v, hook %+v, row %+v; want %+v", decided, hooked, row.Auth, tc.want)
			}
		})
	}
}

// A stuck report records how the reporting caller authenticated.
func TestStuckReportRecordsCallerAuth(t *testing.T) {
	upstream, _ := fakeMCP(t)
	db := newSQLiteTestDB(t)
	svc := newHookedService(t, db, upstream.URL)
	var hooked invocation.Auth
	svc.SetEventHook(func(_ context.Context, _ *sql.Tx, c invocation.Change) error {
		hooked = c.Invocation.Auth
		return nil
	})
	ctx := auth.WithIdentity(context.Background(), auth.Identity{AgentID: "agent-a", Issuer: "https://idp.bank", Method: auth.IdentityMethodJWT})
	resp, err := svc.ReportStuck(ctx, "bank", map[string]any{"task": "pay INV-1", "boundary": "P-02"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := store.NewInvocationRepo(db).Get(context.Background(), resp.InvocationID)
	want := invocation.Auth{Issuer: "https://idp.bank", Method: "jwt"}
	if err != nil || hooked != want || row.Auth != want {
		t.Fatalf("hook %+v, row %+v (%v); want %+v", hooked, row.Auth, err, want)
	}
}
