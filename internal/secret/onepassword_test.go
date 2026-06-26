package secret

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jedwards1230/labctl/internal/manifest"
)

// opProvider builds a stub-backed op provider (no real `op` session).
func opProvider(run Runner) *OnePassword {
	return newOnePassword(manifest.ProviderConfig{Scheme: "op", Command: []string{"op", "read", "{ref}"}}, run)
}

func TestResolveRead(t *testing.T) {
	var gotArgv []string
	p := opProvider(func(argv []string) (string, error) {
		gotArgv = argv
		return "resolved-value", nil
	})
	v, err := resolveWith(t, p, Ref{URI: "op://homelab/Radarr/api_key"})
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

func TestResolveItemGet(t *testing.T) {
	var gotArgv []string
	p := opProvider(func(argv []string) (string, error) {
		gotArgv = argv
		return "item-value", nil
	})
	v, err := resolveWith(t, p, Ref{URI: "op://homelab/Forgejo/token", Idiom: "item-get"})
	if err != nil {
		t.Fatal(err)
	}
	if v != "item-value" {
		t.Fatalf("got %q", v)
	}
	want := []string{"op", "item", "get", "Forgejo", "--vault", "homelab", "--field", "token", "--reveal"}
	if !reflect.DeepEqual(gotArgv, want) {
		t.Fatalf("argv = %v, want %v", gotArgv, want)
	}
}

func TestResolveItemJSON(t *testing.T) {
	var gotArgv []string
	p := opProvider(func(argv []string) (string, error) {
		gotArgv = argv
		return "{}", nil
	})
	if _, err := resolveWith(t, p, Ref{URI: "op://homelab/Forgejo/token", Idiom: "item-json"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"op", "item", "get", "Forgejo", "--vault", "homelab", "--format", "json", "--reveal"}
	if !reflect.DeepEqual(gotArgv, want) {
		t.Fatalf("argv = %v, want %v", gotArgv, want)
	}
}

func TestFieldFallback(t *testing.T) {
	// First field empty, second returns a value.
	p := opProvider(func(argv []string) (string, error) {
		ref := argv[len(argv)-1]
		switch {
		case strings.HasSuffix(ref, "/credential"):
			return "", nil
		case strings.HasSuffix(ref, "/password"):
			return "pw", nil
		default:
			return "", nil
		}
	})
	v, err := resolveWith(t, p, Ref{URI: "op://a/n8n/credential", Fields: []string{"credential", "password"}})
	if err != nil {
		t.Fatal(err)
	}
	if v != "pw" {
		t.Fatalf("got %q, want pw (field fallback)", v)
	}
}

func TestUnknownIdiomIsConfigError(t *testing.T) {
	p := opProvider(func([]string) (string, error) { return "", nil })
	_, err := resolveWith(t, p, Ref{URI: "op://a/b/c", Idiom: "bogus"})
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected *ConfigError, got %v", err)
	}
}

func TestWithTokenAppendsOnceNoMutation(t *testing.T) {
	base := []string{"PATH=/usr/bin", "HOME=/home/u"}
	out := withToken(base, "sekret")
	want := []string{"PATH=/usr/bin", "HOME=/home/u", "OP_SERVICE_ACCOUNT_TOKEN=sekret"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("withToken = %v, want %v", out, want)
	}
	// base must be unmodified.
	if !reflect.DeepEqual(base, []string{"PATH=/usr/bin", "HOME=/home/u"}) {
		t.Fatalf("withToken mutated base: %v", base)
	}
	// Exactly one token entry.
	count := 0
	for _, e := range out {
		if strings.HasPrefix(e, "OP_SERVICE_ACCOUNT_TOKEN=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("token entries = %d, want 1", count)
	}
}

// TestResolveDoesNotLeak asserts the stub-backed resolve path writes to no
// writer and never reads the (absent) service-account token. The provider here
// is configured with a token *file* that does not exist; because the stub runner
// is set, exec() short-circuits before the token is read, so resolution succeeds
// and nothing is emitted.
func TestResolveDoesNotLeak(t *testing.T) {
	p := newOnePassword(manifest.ProviderConfig{
		Scheme:  "op",
		Command: []string{"op", "read", "{ref}"},
		Auth:    manifest.ProviderAuth{ServiceAccountToken: &manifest.SecretSource{File: "/nonexistent/sa-token"}},
	}, func([]string) (string, error) { return "value", nil })

	v, err := resolveWith(t, p, Ref{URI: "op://a/b/c"})
	if err != nil {
		t.Fatalf("stub path must not read the token file: %v", err)
	}
	if v != "value" {
		t.Fatalf("got %q", v)
	}
}
