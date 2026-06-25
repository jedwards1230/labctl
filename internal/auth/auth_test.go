package auth

import (
	"net/http"
	"testing"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/template"
)

// fakeResolver satisfies template.Resolver so the secret→header path can be
// exercised without a real `op`.
type fakeResolver map[string]string

func (f fakeResolver) Secret(name string) (string, error) { return f[name], nil }

func newReq(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestApplyStrategies(t *testing.T) {
	tests := []struct {
		name  string
		auth  manifest.Auth
		check func(t *testing.T, r *http.Request)
	}{
		{
			name: "none explicit is a no-op",
			auth: manifest.Auth{Strategy: "none"},
			check: func(t *testing.T, r *http.Request) {
				if len(r.Header) != 0 {
					t.Errorf("headers = %v, want none", r.Header)
				}
			},
		},
		{
			name: "empty strategy is a no-op",
			auth: manifest.Auth{},
			check: func(t *testing.T, r *http.Request) {
				if len(r.Header) != 0 {
					t.Errorf("headers = %v, want none", r.Header)
				}
			},
		},
		{
			name: "header-key sets the named header",
			auth: manifest.Auth{Strategy: "header-key", Header: "X-Api-Key", Value: "secret123"},
			check: func(t *testing.T, r *http.Request) {
				if got := r.Header.Get("X-Api-Key"); got != "secret123" {
					t.Errorf("X-Api-Key = %q, want secret123", got)
				}
			},
		},
		{
			name: "bearer uses the default scheme",
			auth: manifest.Auth{Strategy: "bearer", Value: "tok"},
			check: func(t *testing.T, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != "Bearer tok" {
					t.Errorf("Authorization = %q, want 'Bearer tok'", got)
				}
			},
		},
		{
			name: "bearer honors a custom scheme",
			auth: manifest.Auth{Strategy: "bearer", Scheme: "Token", Value: "tok"},
			check: func(t *testing.T, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != "Token tok" {
					t.Errorf("Authorization = %q, want 'Token tok'", got)
				}
			},
		},
		{
			name: "basic sets the credential pair",
			auth: manifest.Auth{Strategy: "basic", Username: "u", Password: "p"},
			check: func(t *testing.T, r *http.Request) {
				u, p, ok := r.BasicAuth()
				if !ok || u != "u" || p != "p" {
					t.Errorf("BasicAuth = %q/%q ok=%v, want u/p true", u, p, ok)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newReq(t)
			a := New(tt.auth, template.Env{})
			if err := a.Apply(req, false); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			tt.check(t, req)
		})
	}
}

func TestApplyNoAuthOverride(t *testing.T) {
	req := newReq(t)
	a := New(manifest.Auth{Strategy: "bearer", Value: "tok"}, template.Env{})
	if err := a.Apply(req, true); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("noAuth override set Authorization = %q, want empty", got)
	}
}

func TestApplyResolvesSecretTemplate(t *testing.T) {
	req := newReq(t)
	env := template.Env{Secrets: fakeResolver{"api_key": "resolved-xyz"}}
	a := New(manifest.Auth{Strategy: "header-key", Header: "X-Api-Key", Value: "{secret.api_key}"}, env)
	if err := a.Apply(req, false); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := req.Header.Get("X-Api-Key"); got != "resolved-xyz" {
		t.Errorf("X-Api-Key = %q, want resolved-xyz", got)
	}
}

func TestApplyPropagatesExpandError(t *testing.T) {
	// A {secret.X} with no resolver configured must surface as an error, not a
	// silently-empty credential.
	req := newReq(t)
	a := New(manifest.Auth{Strategy: "header-key", Header: "X-Api-Key", Value: "{secret.missing}"}, template.Env{})
	if err := a.Apply(req, false); err == nil {
		t.Error("want error from unresolved secret, got nil")
	}
}

func TestApplyUnsupportedAndUnknownStrategies(t *testing.T) {
	// Later-phase strategies and a typo both error rather than silently no-op.
	// ws-login is excluded here because it is now implemented (no-op at HTTP
	// layer; auth is connection-scoped in transport.DoJSONRPCWS). See
	// TestApplyWSLoginNoOp.
	// oauth2-client-credentials is excluded because it is now implemented. See
	// TestApplyOAuth2ClientCredentials in oauth2_test.go.
	for _, strat := range []string{
		"login-flow", "external-tool", "bogus",
	} {
		t.Run(strat, func(t *testing.T) {
			req := newReq(t)
			a := New(manifest.Auth{Strategy: strat}, template.Env{})
			if err := a.Apply(req, false); err == nil {
				t.Errorf("strategy %q: want error, got nil", strat)
			}
		})
	}
}

func TestApplyWSLoginNoOp(t *testing.T) {
	// ws-login auth is connection-scoped (handled in transport.DoJSONRPCWS);
	// Apply is a deliberate no-op so it does not error on HTTP requests.
	req := newReq(t)
	a := New(manifest.Auth{Strategy: "ws-login", Method: "auth.login_with_api_key"}, template.Env{})
	if err := a.Apply(req, false); err != nil {
		t.Errorf("ws-login Apply: want nil, got %v", err)
	}
}
