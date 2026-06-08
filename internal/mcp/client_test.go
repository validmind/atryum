package mcp

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
)

func TestConnectionDebugLogsHTTPProbeWithoutSecrets(t *testing.T) {
	var logs bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	}()

	const targetURL = "https://shortcut.example/mcp"
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"token expired"}}`)),
			Request:    r,
		}, nil
	})}, debug: true, sessions: make(map[string]string)}
	result := client.TestConnection(context.Background(), Upstream{
		Name:        "shortcut",
		Mode:        UpstreamModeHTTP,
		BaseURL:     targetURL,
		AuthToken:   "secret-token",
		AuthHeaders: []AuthHeader{{Name: "X-Api-Key", Value: "super-secret"}},
		Enabled:     true,
	})

	if result.Ok {
		t.Fatalf("expected failed auth probe")
	}
	got := logs.String()
	for _, want := range []string{
		"connection test start server=shortcut",
		"target=" + targetURL,
		"auth=has_bearer=true auth_headers=X-Api-Key",
		"connection test http initialize server=shortcut",
		"connection test http response server=shortcut status=401",
		"connection test result server=shortcut",
		"message=\"http 401: token expired\"",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected logs to contain %q, got:\n%s", want, got)
		}
	}
	for _, secret := range []string{"secret-token", "super-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("expected logs to redact %q, got:\n%s", secret, got)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
