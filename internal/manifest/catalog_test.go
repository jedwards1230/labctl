package manifest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// wantCatalog is the set of services the embedded catalog must ship. A change to
// the bundled set is a deliberate edit here, so an accidental add/drop is caught.
var wantCatalog = []string{
	"abs", "authentik", "bazarr", "cloudflare", "contextforge", "forgejo",
	"harbor", "n8n", "prowlarr", "radarr", "sonarr", "sunshine", "tdarr",
	"truenas", "ts",
}

// TestCatalogHasExpectedServices asserts the catalog ships exactly the 15
// expected services.
func TestCatalogHasExpectedServices(t *testing.T) {
	got := CatalogNames()
	want := append([]string(nil), wantCatalog...)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("catalog has %d services %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("catalog[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestCatalogServicesValidate is the CI guard that every embedded manifest is a
// well-formed manifest: it decodes and passes structural Validate. A malformed
// catalog entry fails here rather than at a user's first call.
func TestCatalogServicesValidate(t *testing.T) {
	svcs, err := CatalogServices()
	if err != nil {
		t.Fatalf("CatalogServices: %v", err)
	}
	for _, svc := range svcs {
		t.Run(svc.Name, func(t *testing.T) {
			if svc.Name == "" {
				t.Fatal("embedded service has empty name")
			}
			if err := Validate(svc); err != nil {
				t.Fatalf("Validate(%s): %v", svc.Name, err)
			}
		})
	}
}

// TestCatalogNoLeak guards the catalog's portability: no embedded manifest may
// carry a homelab-specific endpoint, an op:// secret ref, or an active base_url —
// those user-specific bindings belong in profile.yaml, never the portable
// manifest. Mirrors the scaffold no-leak guard.
func TestCatalogNoLeak(t *testing.T) {
	banned := []string{"lilbro.cloud", "192.168.", "10.43.", "op://"}
	for _, name := range CatalogNames() {
		raw, ok := CatalogManifest(name)
		if !ok {
			t.Fatalf("CatalogManifest(%q) missing", name)
		}
		body := string(raw)
		t.Run(name, func(t *testing.T) {
			for _, b := range banned {
				if strings.Contains(body, b) {
					t.Errorf("embedded %s contains banned token %q (keep the catalog portable)", name, b)
				}
			}
			// A portable manifest must not bind base_url — that's profile.yaml's job.
			for _, line := range strings.Split(body, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "base_url:") {
					t.Errorf("embedded %s has an active base_url line: %q", name, line)
				}
			}
		})
	}
}

// TestLoadEmbeddedOnly: an empty config dir (no services/) yields the whole
// embedded catalog, each marked OriginEmbedded.
func TestLoadEmbeddedOnly(t *testing.T) {
	l, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(l.Services) != len(CatalogNames()) {
		t.Fatalf("loaded %d services, want %d embedded", len(l.Services), len(CatalogNames()))
	}
	for _, name := range CatalogNames() {
		if _, ok := l.Services[name]; !ok {
			t.Errorf("embedded service %q did not load", name)
		}
		if got := l.OriginOf(name); got != OriginEmbedded {
			t.Errorf("%s origin = %q, want embedded", name, got)
		}
	}
}

// TestLoadLocalOverridesEmbedded: a local services/<name>.yaml whose name matches
// an embedded service overrides it (the manifest is replaced, marked override) —
// it is NOT a duplicate error.
func TestLoadLocalOverridesEmbedded(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "radarr.yaml", `
name: radarr
description: my overridden radarr
base_url: http://radarr.local
auth: { strategy: none }
commands:
  ping: { method: GET, path: /ping }
`)
	l, err := Load(dir)
	if err != nil {
		t.Fatalf("local override should not error: %v", err)
	}
	if got := l.OriginOf("radarr"); got != OriginOverride {
		t.Fatalf("radarr origin = %q, want override", got)
	}
	svc := l.Services["radarr"]
	if svc.Description != "my overridden radarr" {
		t.Errorf("description = %q, want the local override's", svc.Description)
	}
	// The local manifest fully replaces the embedded one: the local `ping` command
	// is present and the embedded `list`/`status` commands are gone.
	if _, ok := svc.Commands["ping"]; !ok {
		t.Error("override should carry the local ping command")
	}
	if _, ok := svc.Commands["status"]; ok {
		t.Error("override should not inherit the embedded status command")
	}
	// Every other catalog service still loads as embedded.
	if got := l.OriginOf("sonarr"); got != OriginEmbedded {
		t.Errorf("sonarr origin = %q, want embedded (untouched by the radarr override)", got)
	}
}

// TestLoadLocalOnly: a local service with no embedded counterpart loads as
// OriginLocal, alongside the full embedded catalog.
func TestLoadLocalOnly(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "customsvc.yaml", `
name: customsvc
base_url: http://custom.local
auth: { strategy: none }
commands:
  ping: { method: GET, path: /ping }
`)
	l, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := l.OriginOf("customsvc"); got != OriginLocal {
		t.Fatalf("customsvc origin = %q, want local", got)
	}
	if len(l.Services) != len(CatalogNames())+1 {
		t.Errorf("loaded %d services, want %d embedded + 1 local", len(l.Services), len(CatalogNames()))
	}
	if got := l.OriginOf("radarr"); got != OriginEmbedded {
		t.Errorf("radarr origin = %q, want embedded", got)
	}
}

// TestLoadDuplicateLocal: two local files declaring the SAME name (distinct from
// any override of an embedded service) is a real duplicate error.
func TestLoadDuplicateLocal(t *testing.T) {
	dir := t.TempDir()
	body := func(file string) string {
		return `
name: dupe
base_url: http://h
auth: { strategy: none }
commands:
  ping: { method: GET, path: /ping }
`
	}
	writeManifest(t, dir, "a.yaml", body("a"))
	writeManifest(t, dir, "b.yaml", body("b"))
	if _, err := Load(dir); err == nil {
		t.Fatal("two local files with the same name: should be a duplicate error")
	} else if !strings.Contains(err.Error(), "duplicate service name") {
		t.Fatalf("err = %v, want a duplicate-service-name error", err)
	}
}

// TestCatalogServiceUnknown: requesting a non-embedded name errors.
func TestCatalogServiceUnknown(t *testing.T) {
	if _, err := CatalogService("not-a-service"); err == nil {
		t.Fatal("unknown embedded service should error")
	}
}

// TestCatalogManifestRoundTrip: the raw bytes `catalog show` would print decode
// back to the same service (a sanity check on the show path).
func TestCatalogManifestRoundTrip(t *testing.T) {
	raw, ok := CatalogManifest("radarr")
	if !ok {
		t.Fatal("radarr should be embedded")
	}
	tmp := filepath.Join(t.TempDir(), "radarr.yaml")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	svc, err := LoadService(tmp, Config{})
	if err != nil {
		t.Fatalf("LoadService(catalog show output): %v", err)
	}
	if svc.Name != "radarr" {
		t.Errorf("name = %q, want radarr", svc.Name)
	}
}
