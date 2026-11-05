package atryum

import (
	"context"
	"errors"
	"testing"

	"github.com/validmind/atryum/internal/mcp"
)

// An embedder's UpstreamUnavailable reaches Atryum as the internal error that
// makes tools/list and initialize show its tools and notice.
func TestUpstreamUnavailableBecomesTheInternalError(t *testing.T) {
	want := &UpstreamUnavailable{Message: "not connected", Tools: []Tool{{Name: "connect_account"}}, Instructions: "Connect it."}
	provider := upstreamCredentialProvider(func(_ context.Context, c UpstreamContact) (UpstreamCredential, error) {
		if c.Purpose != UpstreamInitialize {
			t.Errorf("purpose = %q", c.Purpose)
		}
		return UpstreamCredential{}, want
	})
	ctx := mcp.WithPurpose(context.Background(), mcp.PurposeInitialize)
	_, err := provider(ctx, mcp.Upstream{Name: "shortcut"})
	var u *mcp.Unavailable
	if !errors.As(err, &u) || u.Message != "not connected" || len(u.Tools) != 1 || u.Tools[0].Name != "connect_account" || u.Instructions != "Connect it." {
		t.Fatalf("err = %#v", err)
	}
	// Any other error is passed through unchanged.
	plain := errors.New("boom")
	provider = upstreamCredentialProvider(func(context.Context, UpstreamContact) (UpstreamCredential, error) { return UpstreamCredential{}, plain })
	if _, err := provider(ctx, mcp.Upstream{}); !errors.Is(err, plain) {
		t.Fatalf("err = %v", err)
	}
}
