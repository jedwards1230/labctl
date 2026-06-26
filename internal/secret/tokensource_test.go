package secret

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jedwards1230/labctl/internal/manifest"
)

func TestTokenNilSourceInheritsSession(t *testing.T) {
	a := opAuth{src: nil}
	tok, err := a.token()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		t.Fatalf("nil source should yield empty token, got %q", tok)
	}
}

func TestTokenValue(t *testing.T) {
	a := opAuth{src: &manifest.SecretSource{Value: "literal-token"}}
	tok, err := a.token()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "literal-token" {
		t.Fatalf("got %q", tok)
	}
}

func TestTokenEnv(t *testing.T) {
	t.Setenv("LABCTL_TEST_SA", "env-token")
	a := opAuth{src: &manifest.SecretSource{Env: "LABCTL_TEST_SA"}}
	tok, err := a.token()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "env-token" {
		t.Fatalf("got %q", tok)
	}
}

func TestTokenEnvEmptyIsAuthError(t *testing.T) {
	t.Setenv("LABCTL_TEST_SA_EMPTY", "")
	a := opAuth{src: &manifest.SecretSource{Env: "LABCTL_TEST_SA_EMPTY"}}
	_, err := a.token()
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *AuthError, got %v", err)
	}
}

func TestTokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sa-token")
	if err := os.WriteFile(path, []byte("  file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := opAuth{src: &manifest.SecretSource{File: path}}
	tok, err := a.token()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "file-token" {
		t.Fatalf("got %q (want trimmed)", tok)
	}
}

func TestTokenFileHomeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "sa-token"), []byte("home-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"~/sa-token", "$HOME/sa-token", "${HOME}/sa-token"} {
		t.Run(ref, func(t *testing.T) {
			a := opAuth{src: &manifest.SecretSource{File: ref}}
			tok, err := a.token()
			if err != nil {
				t.Fatal(err)
			}
			if tok != "home-token" {
				t.Fatalf("ref %q → %q, want home-token", ref, tok)
			}
		})
	}
}

func TestTokenFileUnreadableIsAuthError(t *testing.T) {
	a := opAuth{src: &manifest.SecretSource{File: filepath.Join(t.TempDir(), "missing")}}
	_, err := a.token()
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *AuthError, got %v", err)
	}
}

func TestTokenFileEmptyIsAuthError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := opAuth{src: &manifest.SecretSource{File: path}}
	_, err := a.token()
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *AuthError, got %v", err)
	}
}

func TestTokenSourceViolations(t *testing.T) {
	cases := map[string]manifest.SecretSource{
		"zero-set-present-block": {},
		"two-set":                {Value: "a", Env: "B"},
		"three-set":              {File: "/x", Value: "a", Env: "B"},
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			a := opAuth{src: &src}
			_, err := a.token()
			var cfgErr *ConfigError
			if !errors.As(err, &cfgErr) {
				t.Fatalf("expected *ConfigError, got %v", err)
			}
		})
	}
}
