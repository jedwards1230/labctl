package manifest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadStrictConfigRejectsUnknownKey proves a typo'd top-level config key is
// rejected (strict decoding) and classifies as a *ConfigError (exit 2).
func TestLoadStrictConfigRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "version: 1\nop_vault: vault\n")
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected an error for an unknown top-level config key")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("unknown config key should be a *ConfigError, got %T: %v", err, err)
	}
}

// TestLoadConfigSecretAndSecretsCoexist proves a config carrying BOTH the legacy
// secret: block and the new secrets: block still loads clean under strict decode.
func TestLoadConfigSecretAndSecretsCoexist(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `version: 1
secret:
  resolver: op
  command: ["op", "read", "{ref}"]
  env_override: true
secrets:
  env_override: true
  providers:
    onepassword:
      scheme: op
      command: ["op", "read", "{ref}"]
`)
	if _, err := Load(dir); err != nil {
		t.Fatalf("secret: and secrets: should coexist: %v", err)
	}
}

func writeManifest(t *testing.T, dir, name, body string) {
	t.Helper()
	svcDir := filepath.Join(dir, "services")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svcDir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMergeDefaults(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "tdarr.yaml", `
auth: { strategy: none }
`)
	l, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc, ok := l.Services["tdarr"]
	if !ok {
		t.Fatal("tdarr not loaded (name should default to filename stem)")
	}
	if svc.Transport != "http" {
		t.Errorf("transport default = %q, want http", svc.Transport)
	}
	if svc.Timeout != "60s" {
		t.Errorf("timeout default = %q, want 60s", svc.Timeout)
	}
	if svc.TimeoutDuration().Seconds() != 60 {
		t.Errorf("timeout = %v, want 60s", svc.TimeoutDuration())
	}
}

func TestLoadMissingDir(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	// A missing config dir still yields the embedded catalog (no local overrides).
	if len(l.Services) != len(CatalogNames()) {
		t.Errorf("missing dir loaded %d services, want %d embedded", len(l.Services), len(CatalogNames()))
	}
	if svc, ok := l.Services["radarr"]; !ok {
		t.Error("embedded radarr should be available with no config dir")
	} else if got := l.OriginOf(svc.Name); got != OriginEmbedded {
		t.Errorf("radarr origin = %q, want embedded", got)
	}
}

func TestValidateRejectsBad(t *testing.T) {
	// Fixtures are portable (no base_url/ref) so each case exercises its INTENDED
	// rule — an in-manifest base_url would otherwise short-circuit on the
	// "no in-manifest binding" rule below. The binding-rule cases are explicit.
	cases := map[string]*Service{
		"bad transport":        {Name: "x", Transport: "carrier-pigeon"},
		"bad strategy":         {Name: "x", Auth: Auth{Strategy: "telepathy"}},
		"undeclared secret":    {Name: "x", Auth: Auth{Strategy: "header-key", Header: "X", Value: "{secret.missing}"}},
		"header-key no header": {Name: "x", Auth: Auth{Strategy: "header-key", Value: "v"}},
		"bad svc pagination style": {
			Name:       "x",
			Pagination: Pagination{Style: "infinite-scroll"},
		},
		"bad cmd pagination style": {
			Name: "x",
			Commands: map[string]Command{
				"list": {Method: "GET", Path: "/", Pagination: Pagination{Style: "magic"}},
			},
		},
		// The "no in-manifest binding" rule: base_url / endpoint base_url / secret
		// ref all belong in profile.yaml, never the manifest.
		"in-manifest base_url": {Name: "x", BaseURL: "https://h.example.com"},
		"in-manifest endpoint base_url": {
			Name:      "x",
			Endpoints: map[string]Endpoint{"alt": {BaseURL: "https://alt.example.com"}},
		},
		"in-manifest secret ref": {
			Name:    "x",
			Secrets: map[string]Secret{"api_key": {Ref: "op://vault/X/api_key"}},
		},
	}
	for name, svc := range cases {
		if err := Validate(svc); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

// TestValidateAcceptsGood proves a well-formed PORTABLE manifest passes
// structural Validate: no in-manifest base_url and an env-only secret slot (env
// is a permitted in-manifest override; ref/base_url are not). Completeness
// (a bound base_url + secrets) is ValidateComplete's job, not Validate's.
func TestValidateAcceptsGood(t *testing.T) {
	svc := &Service{
		Name:    "radarr",
		Auth:    Auth{Strategy: "header-key", Header: "X-Api-Key", Value: "{secret.api_key}"},
		Secrets: map[string]Secret{"api_key": {Env: "RADARR_API_KEY"}},
		Commands: map[string]Command{
			"status": {Method: "GET", Path: "/api/v3/system/status"},
		},
	}
	if err := Validate(svc); err != nil {
		t.Fatalf("valid portable manifest rejected: %v", err)
	}
}

func TestSecretRefsExtraction(t *testing.T) {
	got := secretRefs(`{secret.username}:{secret.password}`)
	if len(got) != 2 || got[0] != "username" || got[1] != "password" {
		t.Fatalf("got %v", got)
	}
}
