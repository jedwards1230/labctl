package secret

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/jedwards1230/labctl/internal/manifest"
)

// TestResolvedValuesSnapshot proves the resolver exposes only the non-empty
// values it has actually resolved (and cached), which is what agentsafety's
// NewScrubber consumes to redact diagnostics.
func TestResolvedValuesSnapshot(t *testing.T) {
	run := func(argv []string) (string, error) { return "resolved-val", nil }
	r := New(context.Background(), legacyCfg(), map[string]manifest.Secret{
		"a": {Ref: "op://v/i/a"},
		"b": {Ref: "op://v/i/b"},
	}, "", run)
	r.withGetenv(func(string) string { return "" })

	// Nothing resolved yet.
	if vals := r.ResolvedValues(); len(vals) != 0 {
		t.Fatalf("ResolvedValues before any Secret() = %v, want empty", vals)
	}
	if _, err := r.Secret("a"); err != nil {
		t.Fatal(err)
	}
	vals := r.ResolvedValues()
	sort.Strings(vals)
	if len(vals) != 1 || vals[0] != "resolved-val" {
		t.Fatalf("ResolvedValues = %v, want [resolved-val]", vals)
	}
}

// TestResolverOpFailureIsAuthError proves a provider/`op` failure surfaces as a
// *secret.AuthError so classify() maps it to exit 3 — parity with the
// auth-strategy path, regardless of whether the secret is consumed via
// body/query/path/header/params.
func TestResolverOpFailureIsAuthError(t *testing.T) {
	failOp := func([]string) (string, error) { return "", fmt.Errorf("op: session expired") }
	r := New(context.Background(), legacyCfg(), map[string]manifest.Secret{"k": {Ref: "op://a/b/c"}}, "", failOp)
	r.withGetenv(func(string) string { return "" })
	_, err := r.Secret("k")
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %T (%v), want *secret.AuthError", err, err)
	}
}

// TestResolverEmptyIsAuthError proves a runner that returns "" maps to AuthError.
func TestResolverEmptyIsAuthError(t *testing.T) {
	emptyOp := func([]string) (string, error) { return "", nil }
	r := New(context.Background(), legacyCfg(), map[string]manifest.Secret{"k": {Ref: "op://a/b/c"}}, "", emptyOp)
	r.withGetenv(func(string) string { return "" })
	_, err := r.Secret("k")
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %T (%v), want *secret.AuthError for empty resolution", err, err)
	}
}

// TestResolverUndeclaredIsConfigError proves an undeclared-secret reference maps
// to *secret.ConfigError (exit 2), not a plain error (exit 1).
func TestResolverUndeclaredIsConfigError(t *testing.T) {
	r := New(context.Background(), legacyCfg(), map[string]manifest.Secret{}, "", func([]string) (string, error) { return "x", nil })
	_, err := r.Secret("nope")
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %T (%v), want *secret.ConfigError", err, err)
	}
}

// legacyCfg builds a Config using the legacy top-level secret: block, exercising
// the back-compat normalization path (legacy → single op provider).
func legacyCfg() manifest.Config {
	return manifest.Config{
		Secret: manifest.SecretResolver{Command: []string{"op", "read", "{ref}"}, EnvOverride: true},
	}
}

func TestResolveCaches(t *testing.T) {
	calls := 0
	run := func(argv []string) (string, error) { calls++; return "v", nil }
	r := New(context.Background(), legacyCfg(), map[string]manifest.Secret{"k": {Ref: "op://a/b/c"}}, "", run)
	r.withGetenv(func(string) string { return "" })
	for i := 0; i < 3; i++ {
		if _, err := r.Secret("k"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("resolver called %d times, want 1 (cached)", calls)
	}
}

func TestEnvOverridePrecedence(t *testing.T) {
	run := func(argv []string) (string, error) { return "from-op", nil }
	// Explicit per-secret env wins over the provider.
	r := New(context.Background(), legacyCfg(), map[string]manifest.Secret{"api_key": {Ref: "op://a/b/c", Env: "RADARR_API_KEY"}}, "RADARR", run)
	r.withGetenv(func(k string) string {
		if k == "RADARR_API_KEY" {
			return "from-env"
		}
		return ""
	})
	v, err := r.Secret("api_key")
	if err != nil {
		t.Fatal(err)
	}
	if v != "from-env" {
		t.Fatalf("got %q, want from-env (env should win)", v)
	}
}

func TestEnvPrefixOverride(t *testing.T) {
	run := func(argv []string) (string, error) { return "from-op", nil }
	r := New(context.Background(), legacyCfg(), map[string]manifest.Secret{"api_key": {Ref: "op://a/b/c"}}, "RADARR", run)
	r.withGetenv(func(k string) string {
		if k == "RADARR_API_KEY" {
			return "prefixed"
		}
		return ""
	})
	v, _ := r.Secret("api_key")
	if v != "prefixed" {
		t.Fatalf("got %q, want prefixed (<PREFIX>_<NAME> override)", v)
	}
}

func TestUndeclaredSecret(t *testing.T) {
	r := New(context.Background(), legacyCfg(), map[string]manifest.Secret{}, "", func([]string) (string, error) { return "", nil })
	if _, err := r.Secret("nope"); err == nil {
		t.Fatal("expected error for undeclared secret")
	}
}

// TestBackCompatLegacyBlock proves a config carrying only the legacy secret:
// block (no secrets: providers) still resolves op:// refs through the synthesized
// op provider, and that <PREFIX>_<NAME> overrides keep working.
func TestBackCompatLegacyBlock(t *testing.T) {
	cfg := manifest.Config{
		Secret: manifest.SecretResolver{Command: []string{"op", "read", "{ref}"}, EnvOverride: true},
	}
	var gotArgv []string
	run := func(argv []string) (string, error) { gotArgv = argv; return "from-op", nil }
	r := New(context.Background(), cfg, map[string]manifest.Secret{"api_key": {Ref: "op://vault/Radarr/api_key"}}, "RADARR", run)

	// No env set: provider resolves.
	r.withGetenv(func(string) string { return "" })
	v, err := r.Secret("api_key")
	if err != nil {
		t.Fatal(err)
	}
	if v != "from-op" {
		t.Fatalf("got %q, want from-op", v)
	}
	if strings.Join(gotArgv, " ") != "op read op://vault/Radarr/api_key" {
		t.Fatalf("argv = %v", gotArgv)
	}

	// <PREFIX>_<NAME> override wins (fresh resolver to avoid the cache).
	r2 := New(context.Background(), cfg, map[string]manifest.Secret{"api_key": {Ref: "op://vault/Radarr/api_key"}}, "RADARR", run)
	r2.withGetenv(func(k string) string {
		if k == "RADARR_API_KEY" {
			return "overridden"
		}
		return ""
	})
	v2, err := r2.Secret("api_key")
	if err != nil {
		t.Fatal(err)
	}
	if v2 != "overridden" {
		t.Fatalf("got %q, want overridden (<PREFIX>_<NAME> should win)", v2)
	}
}
