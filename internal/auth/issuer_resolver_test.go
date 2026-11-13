package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"

	"github.com/validmind/atryum/pkg/tenant"
)

const runtimeIssuer = "https://runtime.example"

// resolverFor answers for runtimeIssuer with idp's JWKS and counts its calls.
func resolverFor(idp *testIdP, calls *int) IssuerResolver {
	return func(_ context.Context, issuer string) (Config, bool, error) {
		*calls++
		if issuer != runtimeIssuer {
			return Config{}, false, nil
		}
		return Config{Enabled: true, Issuer: issuer + "/", Audience: "atryum", JWKSURL: idp.jwksURL(), AdminEnabled: true, AdminClientID: "x"}, true, nil
	}
}

func runtimeClaims(iss string) jwt.MapClaims {
	c := validClaims()
	c["iss"] = iss
	return c
}

func TestIssuerResolver(t *testing.T) {
	idp := newTestIdP(t)
	ctx := context.Background()

	t.Run("resolved issuer verifies", func(t *testing.T) {
		var calls int
		v := (*Validator)(nil).WithIssuerResolver(resolverFor(idp, &calls))
		id, err := v.Validate(ctx, idp.sign(t, runtimeClaims(runtimeIssuer)))
		if err != nil || id.AgentID != "agent-1" || id.Issuer != runtimeIssuer {
			t.Fatalf("Validate = %+v, %v", id, err)
		}
	})

	t.Run("issuer the resolver doesn't know is refused", func(t *testing.T) {
		var calls int
		v := (*Validator)(nil).WithIssuerResolver(resolverFor(idp, &calls))
		if _, err := v.Validate(ctx, idp.sign(t, runtimeClaims("https://other.example"))); err == nil || calls != 1 {
			t.Fatalf("err = %v, calls = %d", err, calls)
		}
	})

	t.Run("resolver error is refused", func(t *testing.T) {
		v := (*Validator)(nil).WithIssuerResolver(func(context.Context, string) (Config, bool, error) {
			return Config{Enabled: true, Issuer: runtimeIssuer, Audience: "atryum", JWKSURL: idp.jwksURL()}, true, errors.New("db down")
		})
		_, err := v.Validate(ctx, idp.sign(t, runtimeClaims(runtimeIssuer)))
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Result != ResultInvalid || ve.Description != "cannot resolve issuer" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("resolved config must be enabled, for that issuer and audience", func(t *testing.T) {
		for name, mutate := range map[string]func(*Config){
			"disabled":       func(c *Config) { c.Enabled = false },
			"other issuer":   func(c *Config) { c.Issuer = "https://other.example" },
			"other audience": func(c *Config) { c.Audience = "not-atryum" },
			"no audience":    func(c *Config) { c.Audience = "" },
			"no JWKS URL":    func(c *Config) { c.JWKSURL = "" },
		} {
			v := (*Validator)(nil).WithIssuerResolver(func(context.Context, string) (Config, bool, error) {
				c := Config{Enabled: true, Issuer: runtimeIssuer, Audience: "atryum", JWKSURL: idp.jwksURL()}
				mutate(&c)
				return c, true, nil
			})
			if _, err := v.Validate(ctx, idp.sign(t, runtimeClaims(runtimeIssuer))); err == nil {
				t.Errorf("%s: verified", name)
			}
		}
	})

	t.Run("iss is read exactly", func(t *testing.T) {
		var calls int
		v := (*Validator)(nil).WithIssuerResolver(resolverFor(idp, &calls))
		claims := validClaims()
		delete(claims, "iss")
		claims["ISS"] = runtimeIssuer
		if _, err := v.Validate(ctx, idp.sign(t, claims)); err == nil || calls != 0 {
			t.Fatalf("err = %v, calls = %d", err, calls)
		}
	})

	t.Run("static list wins for its own issuers", func(t *testing.T) {
		var calls int
		v := newValidatorForIdP(t, idp).WithIssuerResolver(func(_ context.Context, issuer string) (Config, bool, error) {
			calls++
			return Config{Enabled: true, Issuer: issuer, Audience: "other-aud", JWKSURL: idp.jwksURL()}, true, nil
		})
		if _, err := v.Validate(ctx, idp.sign(t, validClaims())); err != nil {
			t.Fatalf("static token: %v", err)
		}
		other := validClaims()
		other["aud"] = "other-aud"
		if _, err := v.Validate(ctx, idp.sign(t, other)); err == nil {
			t.Fatal("resolver overrode the static issuer's audience")
		}
		if calls != 0 {
			t.Fatalf("resolver called %d times for a static issuer", calls)
		}
	})

	t.Run("resolved issuer never allows login", func(t *testing.T) {
		var calls int
		v := (*Validator)(nil).WithIssuerResolver(resolverFor(idp, &calls))
		token := idp.sign(t, runtimeClaims(runtimeIssuer))
		if _, err := v.ValidateUser(ctx, token); err == nil {
			t.Fatal("ValidateUser accepted a resolved issuer")
		}
		if _, err := v.ValidateAdmin(ctx, token); err == nil {
			t.Fatal("ValidateAdmin accepted a resolved issuer")
		}
	})

	t.Run("signing keys are not shared between tenants", func(t *testing.T) {
		// Two tenants each have an issuer at the same URL with their own keys
		// (same kid). A cache keyed by issuer alone would check tenant b's
		// tokens against tenant a's keys.
		a, b := newTestIdP(t), newTestIdP(t)
		v := (*Validator)(nil).WithIssuerResolver(func(ctx context.Context, issuer string) (Config, bool, error) {
			idp := map[string]*testIdP{"a": a, "b": b}[tenant.From(ctx)]
			return Config{Enabled: true, Issuer: issuer, Audience: "atryum", JWKSURL: idp.jwksURL()}, true, nil
		})
		ctxA, ctxB := tenant.With(ctx, "a"), tenant.With(ctx, "b")
		if _, err := v.Validate(ctxA, a.sign(t, runtimeClaims(runtimeIssuer))); err != nil {
			t.Fatalf("tenant a: %v", err)
		}
		if _, err := v.Validate(ctxB, b.sign(t, runtimeClaims(runtimeIssuer))); err != nil {
			t.Fatalf("tenant b: %v", err)
		}
		if _, err := v.Validate(ctxB, a.sign(t, runtimeClaims(runtimeIssuer))); err == nil {
			t.Fatal("tenant a's token verified as tenant b")
		}
	})

	t.Run("a changed JWKS URL takes effect", func(t *testing.T) {
		// Same tenant and issuer; the issuer moves its keys to a new URL.
		old, rotated := newTestIdP(t), newTestIdP(t)
		current := old
		v := (*Validator)(nil).WithIssuerResolver(func(_ context.Context, issuer string) (Config, bool, error) {
			return Config{Enabled: true, Issuer: issuer, Audience: "atryum", JWKSURL: current.jwksURL()}, true, nil
		})
		if _, err := v.Validate(ctx, old.sign(t, runtimeClaims(runtimeIssuer))); err != nil {
			t.Fatalf("before the change: %v", err)
		}
		current = rotated
		if _, err := v.Validate(ctx, rotated.sign(t, runtimeClaims(runtimeIssuer))); err != nil {
			t.Fatalf("after the change: %v", err)
		}
	})

	t.Run("one tenant's slow issuer doesn't delay another's", func(t *testing.T) {
		// Tenant b's issuer has no JWKS URL and its discovery endpoint hangs.
		// Discovery would run under the validator's lock and stall tenant a;
		// a resolved config without a JWKS URL is refused instead.
		slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(2 * time.Second) }))
		t.Cleanup(slow.Close)
		a := newTestIdP(t)
		v := (*Validator)(nil).WithIssuerResolver(func(ctx context.Context, issuer string) (Config, bool, error) {
			if tenant.From(ctx) == "b" {
				return Config{Enabled: true, Issuer: issuer, Audience: "atryum"}, true, nil
			}
			return Config{Enabled: true, Issuer: issuer, Audience: "atryum", JWKSURL: a.jwksURL()}, true, nil
		})
		ctxA := tenant.With(ctx, "a")
		tokenA := a.sign(t, runtimeClaims(runtimeIssuer))
		if _, err := v.Validate(ctxA, tokenA); err != nil {
			t.Fatalf("tenant a: %v", err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := v.Validate(tenant.With(ctx, "b"), a.sign(t, runtimeClaims(slow.URL)))
			done <- err
		}()
		time.Sleep(50 * time.Millisecond)
		start := time.Now()
		if _, err := v.Validate(ctxA, tokenA); err != nil {
			t.Fatalf("tenant a, b in flight: %v", err)
		}
		if d := time.Since(start); d > 500*time.Millisecond {
			t.Fatalf("tenant a took %v while tenant b's issuer hung", d)
		}
		if err := <-done; err == nil {
			t.Fatal("tenant b's issuer without a JWKS URL verified")
		}
	})
}
