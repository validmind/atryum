package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/invocation"
)

// A gateway's key keeps its capabilities and vendor through create and
// resolve, so the runtime can tell an asserter from an ordinary agent key.
func TestAPIKeyCapabilitiesRoundTrip(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	ctx := context.Background()
	keys := NewAPIKeysRepo(db)
	seedAgent(t, db, "gw-truefoundry")

	if _, err := keys.Create(ctx, APIKey{AgentID: "gw-truefoundry", Name: "gateway", KeyHash: "hash-gw", KeyPrefix: "atr_gw", Capabilities: []string{" asserter ", ""}, GatewayVendor: "truefoundry"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := keys.Create(ctx, APIKey{AgentID: "gw-truefoundry", Name: "plain", KeyHash: "hash-plain", KeyPrefix: "atr_pl"}); err != nil {
		t.Fatalf("Create plain: %v", err)
	}
	got, err := keys.ResolveActive(ctx, "hash-gw", time.Now().UTC())
	if err != nil {
		t.Fatalf("ResolveActive: %v", err)
	}
	if !reflect.DeepEqual(got.Capabilities, []string{"asserter"}) || got.GatewayVendor != "truefoundry" {
		t.Fatalf("capabilities=%v vendor=%q, want [asserter] truefoundry", got.Capabilities, got.GatewayVendor)
	}
	plain, err := keys.ResolveActive(ctx, "hash-plain", time.Now().UTC())
	if err != nil {
		t.Fatalf("ResolveActive plain: %v", err)
	}
	if len(plain.Capabilities) != 0 || plain.GatewayVendor != "" {
		t.Fatalf("plain key: capabilities=%v vendor=%q, want none", plain.Capabilities, plain.GatewayVendor)
	}
}

// The identity a gateway asserted, and its check key, survive a write and a
// read; an ordinary invocation reads back with no asserted identity.
func TestInvocationAssertedIdentityRoundTrip(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	ctx := context.Background()
	repo := NewInvocationRepo(db)
	now := time.Now().UTC().Truncate(time.Second)

	asserted := &invocation.AssertedIdentity{UserID: "emma@acme.com", UserType: "user", UserDisplay: "Emma Ruiz", AgentID: "support-bot", SessionID: "sess-1", By: "gw-truefoundry"}
	gw := invocation.Invocation{InvocationID: "inv_gw", Tool: "close_ticket", Status: invocation.StatusReceived, Input: []byte(`{}`), SubmittedAt: now, Asserted: asserted, GatewayCheckKey: "gck-1"}
	if err := repo.Create(ctx, gw); err != nil {
		t.Fatalf("Create gateway invocation: %v", err)
	}
	plain := invocation.Invocation{InvocationID: "inv_plain", Tool: "close_ticket", Status: invocation.StatusReceived, Input: []byte(`{}`), SubmittedAt: now}
	if err := repo.Create(ctx, plain); err != nil {
		t.Fatalf("Create plain invocation: %v", err)
	}

	got, err := repo.Get(ctx, "inv_gw")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Asserted == nil || *got.Asserted != *asserted || got.GatewayCheckKey != "gck-1" {
		t.Fatalf("asserted=%+v key=%q, want %+v gck-1", got.Asserted, got.GatewayCheckKey, asserted)
	}
	other, err := repo.Get(ctx, "inv_plain")
	if err != nil {
		t.Fatalf("Get plain: %v", err)
	}
	if other.Asserted != nil || other.GatewayCheckKey != "" {
		t.Fatalf("plain invocation: asserted=%+v key=%q, want none", other.Asserted, other.GatewayCheckKey)
	}

	list, _, err := repo.List(ctx, invocation.InvocationListFilter{GatewayCheckKey: "gck-1", Status: string(invocation.StatusReceived)})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].InvocationID != "inv_gw" {
		t.Fatalf("List by check key = %v, want [inv_gw]", list)
	}
}
