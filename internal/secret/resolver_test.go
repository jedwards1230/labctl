package secret

import (
	"strings"
	"testing"

	"github.com/jedwards1230/labctl/internal/manifest"
)

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
	r := New(legacyCfg(), map[string]manifest.Secret{"k": {Ref: "op://a/b/c"}}, "", run)
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
	r := New(legacyCfg(), map[string]manifest.Secret{"api_key": {Ref: "op://a/b/c", Env: "RADARR_API_KEY"}}, "RADARR", run)
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
	r := New(legacyCfg(), map[string]manifest.Secret{"api_key": {Ref: "op://a/b/c"}}, "RADARR", run)
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
	r := New(legacyCfg(), map[string]manifest.Secret{}, "", func([]string) (string, error) { return "", nil })
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
	r := New(cfg, map[string]manifest.Secret{"api_key": {Ref: "op://homelab/Radarr/api_key"}}, "RADARR", run)

	// No env set: provider resolves.
	r.withGetenv(func(string) string { return "" })
	v, err := r.Secret("api_key")
	if err != nil {
		t.Fatal(err)
	}
	if v != "from-op" {
		t.Fatalf("got %q, want from-op", v)
	}
	if strings.Join(gotArgv, " ") != "op read op://homelab/Radarr/api_key" {
		t.Fatalf("argv = %v", gotArgv)
	}

	// <PREFIX>_<NAME> override wins (fresh resolver to avoid the cache).
	r2 := New(cfg, map[string]manifest.Secret{"api_key": {Ref: "op://homelab/Radarr/api_key"}}, "RADARR", run)
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
