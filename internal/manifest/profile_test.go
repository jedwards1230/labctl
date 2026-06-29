package manifest

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProfile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadProfileMissing proves an absent profile.yaml is not an error — it
// yields (nil, nil), so a manifest-only config behaves unchanged.
func TestLoadProfileMissing(t *testing.T) {
	p, err := LoadProfile(t.TempDir())
	if err != nil {
		t.Fatalf("missing profile should not error: %v", err)
	}
	if p != nil {
		t.Fatalf("missing profile should be nil, got %+v", p)
	}
}

// TestLoadProfileMalformed proves a typo'd field is a *ConfigError (exit 2)
// under strict decoding.
func TestLoadProfileMalformed(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "version: 1\nserivces: {}\n") // typo: serivces
	_, err := LoadProfile(dir)
	if err == nil {
		t.Fatal("expected an error for an unknown profile field")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("malformed profile should be a *ConfigError, got %T: %v", err, err)
	}
}

// TestValidateComplete covers the completeness counterpart to the structural
// Validate: a portable service is incomplete until bound, and binding makes it
// pass; a missing endpoint base_url errors. All failures are *ConfigError.
func TestValidateComplete(t *testing.T) {
	portable := func() *Service {
		return &Service{
			Name: "portable",
			Auth: Auth{Strategy: "header-key", Header: "X-Api-Key", Value: "{secret.api_key}"},
			// Declared slot, neither ref nor env → unbound.
			Secrets: map[string]Secret{"api_key": {}},
		}
	}

	// Structurally valid but incomplete (no base_url, unbound secret).
	svc := portable()
	if err := Validate(svc); err != nil {
		t.Fatalf("portable manifest should pass structural Validate: %v", err)
	}
	err := ValidateComplete(svc)
	if err == nil {
		t.Fatal("portable+unbound service should fail ValidateComplete")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("want *ConfigError, got %T: %v", err, err)
	}

	// After binding base_url + the secret ref, it passes.
	bound := portable()
	applyProfile(bound, ServiceBinding{
		BaseURL: "https://demo.example.com",
		Secrets: map[string]SecretBinding{"api_key": {Ref: "op://vault/Demo/api_key"}},
	})
	if err := ValidateComplete(bound); err != nil {
		t.Fatalf("bound service should pass ValidateComplete: %v", err)
	}

	// An endpoint missing base_url is incomplete.
	epSvc := &Service{
		Name:      "ep",
		Endpoints: map[string]Endpoint{"alt": {}},
	}
	if err := ValidateComplete(epSvc); err == nil {
		t.Fatal("endpoint without base_url should fail ValidateComplete")
	} else if !errors.As(err, &cfgErr) {
		t.Fatalf("want *ConfigError, got %T: %v", err, err)
	}
}

// TestLoadAppliesProfile proves Load applies a profile binding onto a portable
// manifest: base_url + secret ref + a merged var land, while a manifest field
// the profile never mentions is left untouched.
func TestLoadAppliesProfile(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "demo.yaml", `
name: demo
env_prefix: DEMO
vars:
  region: us
  keep: original
auth:
  strategy: header-key
  header: X-Api-Key
  value: "{secret.api_key}"
secrets:
  api_key:
    env: DEMO_API_KEY
commands:
  status:
    method: GET
    path: /api/status
`)
	writeProfile(t, dir, `
version: 1
services:
  demo:
    base_url: https://demo.example.com
    vars:
      region: eu
    secrets:
      api_key: { ref: "op://vault/Demo/api_key" }
`)

	l, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := l.Services["demo"]
	if svc == nil {
		t.Fatal("demo not loaded")
	}
	if svc.BaseURL != "https://demo.example.com" {
		t.Errorf("base_url = %q, want the profile's value", svc.BaseURL)
	}
	if got := svc.Secrets["api_key"].Ref; got != "op://vault/Demo/api_key" {
		t.Errorf("secret ref = %q, want the profile's ref", got)
	}
	// The manifest's env survives the per-key secret merge (profile set only ref).
	if got := svc.Secrets["api_key"].Env; got != "DEMO_API_KEY" {
		t.Errorf("secret env = %q, want manifest's DEMO_API_KEY preserved", got)
	}
	if svc.Vars["region"] != "eu" {
		t.Errorf("var region = %q, want profile's eu", svc.Vars["region"])
	}
	// A var the profile never mentions is untouched.
	if svc.Vars["keep"] != "original" {
		t.Errorf("var keep = %q, want manifest's original", svc.Vars["keep"])
	}
	if l.Profile == nil {
		t.Error("Loaded.Profile should be set when profile.yaml is present")
	}
	// And the bound service is complete.
	if err := ValidateComplete(svc); err != nil {
		t.Fatalf("bound demo should pass ValidateComplete: %v", err)
	}
}

// TestLoadProfileSuppliesBaseURL proves the surviving binding precedence now that
// a manifest can no longer carry base_url: a portable manifest gets its base_url
// solely from profile.yaml. (The env > profile half of the chain — a
// <PREFIX>_URL override beating the profile — is proven end-to-end in the
// engine/dispatch layer, where the override lives; see
// TestDispatchEnvURLBeatsProfile.)
func TestLoadProfileSuppliesBaseURL(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "radarr.yaml", `
name: radarr
auth: { strategy: none }
commands:
  status: { method: GET, path: /s }
`)
	writeProfile(t, dir, `
version: 1
services:
  radarr:
    base_url: https://movies.my-lan.example
`)

	l, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Services["radarr"].BaseURL; got != "https://movies.my-lan.example" {
		t.Errorf("base_url = %q, want the profile's binding", got)
	}
}

// TestLoadRejectsInManifestBinding proves the structural "no in-manifest
// binding" rule: a manifest carrying a base_url, an endpoint base_url, or a
// secret ref is rejected by Load with a *ConfigError (exit-2 class), and the
// diagnostic points at profile.yaml — the sole binding mechanism.
func TestLoadRejectsInManifestBinding(t *testing.T) {
	cases := map[string]string{
		"service base_url": `
name: x
base_url: https://x.example.com
auth: { strategy: none }
commands:
  s: { method: GET, path: /s }
`,
		"endpoint base_url": `
name: x
auth: { strategy: none }
endpoints:
  alt:
    base_url: https://alt.example.com
commands:
  s: { method: GET, path: /s }
`,
		"secret ref": `
name: x
auth: { strategy: none }
secrets:
  api_key:
    ref: "op://vault/X/api_key"
commands:
  s: { method: GET, path: /s }
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeManifest(t, dir, "x.yaml", body)
			_, err := Load(dir)
			if err == nil {
				t.Fatal("expected rejection of in-manifest binding")
			}
			var cfgErr *ConfigError
			if !errors.As(err, &cfgErr) {
				t.Fatalf("want *ConfigError (exit-2 class), got %T: %v", err, err)
			}
			if !strings.Contains(err.Error(), "profile.yaml") {
				t.Errorf("diagnostic should point at profile.yaml, got: %v", err)
			}
		})
	}
}

// TestLoadServiceRejectsInManifestBinding proves the same rule fires on the
// single-file path (`lint <file>` → LoadService), not just the config-dir Load.
func TestLoadServiceRejectsInManifestBinding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.yaml")
	if err := os.WriteFile(path, []byte(`
name: x
base_url: https://x.example.com
auth: { strategy: none }
commands:
  s: { method: GET, path: /s }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadService(path, Config{})
	if err == nil {
		t.Fatal("expected LoadService to reject an in-manifest base_url")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("want *ConfigError (exit-2 class), got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "profile.yaml") {
		t.Errorf("diagnostic should point at profile.yaml, got: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

// TestApplyProfileTLSAndSlots covers the per-field overlay edges: a *bool
// tls_insecure overriding true→false, an unset (nil) tls_insecure leaving the
// manifest value intact, and a profile CREATING an endpoint slot and a secret
// slot the manifest never declared.
func TestApplyProfileTLSAndSlots(t *testing.T) {
	t.Run("tls false overrides manifest true", func(t *testing.T) {
		svc := &Service{Name: "x", TLSInsecure: true}
		applyProfile(svc, ServiceBinding{TLSInsecure: ptr(false)})
		if svc.TLSInsecure {
			t.Error("profile tls_insecure: false should override manifest true")
		}
	})

	t.Run("nil tls leaves manifest intact", func(t *testing.T) {
		svc := &Service{Name: "x", TLSInsecure: true}
		applyProfile(svc, ServiceBinding{}) // TLSInsecure nil
		if !svc.TLSInsecure {
			t.Error("unset profile tls_insecure should leave manifest true intact")
		}
	})

	t.Run("creates endpoint and secret slots", func(t *testing.T) {
		svc := &Service{Name: "x"} // no endpoints, no secrets declared
		applyProfile(svc, ServiceBinding{
			Endpoints: map[string]EndpointBinding{
				"alt": {BaseURL: "https://alt.example.com", TLSInsecure: ptr(true)},
			},
			Secrets: map[string]SecretBinding{
				"api_key": {Ref: "op://vault/X/api_key", Env: "X_API_KEY"},
			},
		})
		ep, ok := svc.Endpoints["alt"]
		if !ok {
			t.Fatal("profile should create the endpoint slot the manifest omitted")
		}
		if ep.BaseURL != "https://alt.example.com" || !ep.TLSInsecure {
			t.Errorf("created endpoint = %+v, want base_url + tls_insecure set", ep)
		}
		sec, ok := svc.Secrets["api_key"]
		if !ok {
			t.Fatal("profile should create the secret slot the manifest omitted")
		}
		if sec.Ref != "op://vault/X/api_key" || sec.Env != "X_API_KEY" {
			t.Errorf("created secret = %+v, want ref + env set", sec)
		}
	})
}

// TestLoadProfileEmptyFile asserts the real contract for an empty profile.yaml
// (io.EOF on decode): a non-nil, empty profile (version defaulted to 1, no
// bindings), and that Load still succeeds with such a file present.
func TestLoadProfileEmptyFile(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "")

	p, err := LoadProfile(dir)
	if err != nil {
		t.Fatalf("empty profile should not error: %v", err)
	}
	if p == nil {
		t.Fatal("empty profile should be non-nil")
	}
	if p.Version != 1 {
		t.Errorf("version = %d, want defaulted 1", p.Version)
	}
	if len(p.Services) != 0 {
		t.Errorf("empty profile should carry no bindings, got %d", len(p.Services))
	}

	// And a config dir with an empty profile.yaml still loads (portable manifest).
	writeManifest(t, dir, "demo.yaml", "name: demo\nauth: { strategy: none }\ncommands:\n  s: { method: GET, path: /s }\n")
	if _, err := Load(dir); err != nil {
		t.Fatalf("Load with an empty profile.yaml should succeed: %v", err)
	}
}

// TestWarnOrphanProfileBindings proves a binding for a service that never loaded
// emits a non-fatal warning naming the missing service (sorted, deterministic),
// while a matched binding produces no warning.
func TestWarnOrphanProfileBindings(t *testing.T) {
	profile := &Profile{Services: map[string]ServiceBinding{
		"ghost":  {BaseURL: "https://ghost.example.com"},
		"radarr": {BaseURL: "https://movies.example.com"},
	}}
	services := map[string]*Service{"radarr": {Name: "radarr"}}

	var buf bytes.Buffer
	warnOrphanProfileBindings(profile, services, &buf)
	out := buf.String()
	if !strings.Contains(out, `profile binds unknown service "ghost"`) {
		t.Errorf("expected an orphan warning for ghost, got: %q", out)
	}
	if !strings.Contains(out, "no services/ghost.yaml") {
		t.Errorf("warning should name the missing manifest path, got: %q", out)
	}
	if strings.Contains(out, "radarr") {
		t.Errorf("a matched binding should not warn, got: %q", out)
	}
}
