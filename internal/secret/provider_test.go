package secret

import (
	"context"
	"errors"
	"testing"

	"github.com/jedwards1230/labctl/internal/manifest"
)

func TestSchemeOf(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"op://vault/Radarr/api_key", "op"},
		{"aws://secretsmanager/foo", "aws"},
		{"vault://kv/data/foo", "vault"},
		{"no-scheme-here", ""},
		{"", ""},
		{"://weird", ""},
	}
	for _, c := range cases {
		t.Run(c.ref, func(t *testing.T) {
			if got := schemeOf(c.ref); got != c.want {
				t.Fatalf("schemeOf(%q) = %q, want %q", c.ref, got, c.want)
			}
		})
	}
}

func TestRegistryDispatchOp(t *testing.T) {
	sc := manifest.NormalizeSecrets(manifest.Config{})
	reg := NewRegistry(sc, func([]string) (string, error) { return "", nil })
	p, ok := reg.For("op")
	if !ok {
		t.Fatal("expected an op provider")
	}
	if p.Scheme() != "op" {
		t.Fatalf("provider scheme = %q, want op", p.Scheme())
	}
	if _, ok := reg.For("aws"); ok {
		t.Fatal("did not expect an aws provider")
	}
}

func TestUnknownSchemeIsConfigError(t *testing.T) {
	// A ref whose scheme has no registered provider surfaces a *ConfigError.
	r := New(context.Background(), manifest.Config{},
		map[string]manifest.Secret{"k": {Ref: "vault://kv/data/x"}},
		"", func([]string) (string, error) { return "v", nil })
	r.withGetenv(func(string) string { return "" })
	_, err := r.Secret("k")
	if err == nil {
		t.Fatal("expected an error for an unregistered scheme")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("error %v is not a *ConfigError", err)
	}
}

func TestProviderConfigErrorUnwrap(t *testing.T) {
	inner := errors.New("boom")
	ce := &ConfigError{Err: inner}
	if !errors.Is(ce, inner) {
		t.Fatal("ConfigError should unwrap to its inner error")
	}
	ae := &AuthError{Err: inner}
	if !errors.Is(ae, inner) {
		t.Fatal("AuthError should unwrap to its inner error")
	}
}

// resolveWith is a small helper for onepassword tests.
func resolveWith(t *testing.T, p *OnePassword, ref Ref) (string, error) {
	t.Helper()
	return p.Resolve(context.Background(), ref)
}
