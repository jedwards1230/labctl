package secret

import (
	"strings"
	"testing"

	"github.com/jedwards1230/labctl/internal/manifest"
)

func resolverSpec() manifest.SecretResolver {
	return manifest.SecretResolver{Command: []string{"op", "read", "{ref}"}, EnvOverride: true}
}

func TestResolveRead(t *testing.T) {
	var gotArgv []string
	run := func(argv []string) (string, error) {
		gotArgv = argv
		return "resolved-value", nil
	}
	r := New(resolverSpec(),
		map[string]manifest.Secret{"api_key": {Ref: "op://homelab/Radarr/api_key"}},
		"RADARR", run)
	r.withGetenv(func(string) string { return "" })

	v, err := r.Secret("api_key")
	if err != nil {
		t.Fatal(err)
	}
	if v != "resolved-value" {
		t.Fatalf("got %q", v)
	}
	if strings.Join(gotArgv, " ") != "op read op://homelab/Radarr/api_key" {
		t.Fatalf("argv = %v", gotArgv)
	}
}

func TestResolveCaches(t *testing.T) {
	calls := 0
	run := func(argv []string) (string, error) { calls++; return "v", nil }
	r := New(resolverSpec(), map[string]manifest.Secret{"k": {Ref: "op://a/b/c"}}, "", run)
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
	// Explicit per-secret env wins over the resolver.
	r := New(resolverSpec(), map[string]manifest.Secret{"api_key": {Ref: "op://a/b/c", Env: "RADARR_API_KEY"}}, "RADARR", run)
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
	r := New(resolverSpec(), map[string]manifest.Secret{"api_key": {Ref: "op://a/b/c"}}, "RADARR", run)
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

func TestFieldFallback(t *testing.T) {
	// First field empty, second returns a value.
	run := func(argv []string) (string, error) {
		ref := argv[len(argv)-1]
		if strings.HasSuffix(ref, "/credential") {
			return "", nil
		}
		if strings.HasSuffix(ref, "/password") {
			return "pw", nil
		}
		return "", nil
	}
	r := New(resolverSpec(),
		map[string]manifest.Secret{"k": {Ref: "op://a/n8n/credential", Fields: []string{"credential", "password"}}},
		"", run)
	r.withGetenv(func(string) string { return "" })
	v, err := r.Secret("k")
	if err != nil {
		t.Fatal(err)
	}
	if v != "pw" {
		t.Fatalf("got %q, want pw (field fallback)", v)
	}
}

func TestUndeclaredSecret(t *testing.T) {
	r := New(resolverSpec(), map[string]manifest.Secret{}, "", func([]string) (string, error) { return "", nil })
	if _, err := r.Secret("nope"); err == nil {
		t.Fatal("expected error for undeclared secret")
	}
}
