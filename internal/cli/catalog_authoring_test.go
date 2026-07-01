package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/jedwards1230/labctl/internal/agentsafety"
	"github.com/jedwards1230/labctl/internal/manifest"
)

// TestCatalogEditSeedsFullCopy: `catalog edit <name>` writes the COMPLETE embedded
// manifest (byte-for-byte) into <config-dir>/services/<name>.yaml and prints the
// absolute path to stdout. A full copy is required because a local override
// wholesale replaces the embedded entry (no field-level merge).
func TestCatalogEditSeedsFullCopy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "edit", "authentik"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}

	dest := filepath.Join(dir, "services", "authentik.yaml")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("override not written: %v", err)
	}
	want, ok := manifest.CatalogManifest("authentik")
	if !ok {
		t.Fatal("authentik missing from embedded catalog")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("seeded override is not a full byte-for-byte copy of the embedded manifest")
	}
	// The printed path is the data output (stdout), absolute, pointing at dest.
	absDest, _ := filepath.Abs(dest)
	if gotPath := bytes.TrimSpace(out.Bytes()); string(gotPath) != absDest {
		t.Errorf("stdout = %q, want the absolute path %q", gotPath, absDest)
	}
}

// TestCatalogEditRefusesClobber: an existing override is not overwritten without
// --force (exit usage); with --force it is replaced by the embedded manifest.
func TestCatalogEditRefusesClobber(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", dir)
	dest := filepath.Join(dir, "services", "authentik.yaml")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	const sentinel = "name: authentik\n# in-progress edit\n"
	if err := os.WriteFile(dest, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	// Without --force: usage error, sentinel untouched.
	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "edit", "authentik"}, &out, &errb); code != agentsafety.ExitUsage {
		t.Fatalf("exit = %d, want %d (usage) (stderr: %s)", code, agentsafety.ExitUsage, errb.String())
	}
	if got, _ := os.ReadFile(dest); string(got) != sentinel {
		t.Errorf("override was clobbered without --force: %q", got)
	}

	// With --force: replaced by the embedded manifest.
	out.Reset()
	errb.Reset()
	if code := Run([]string{"catalog", "edit", "authentik", "--force"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("--force exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	got, _ := os.ReadFile(dest)
	want, _ := manifest.CatalogManifest("authentik")
	if !bytes.Equal(got, want) {
		t.Errorf("--force did not replace the override with the embedded manifest")
	}
}

// TestCatalogEditUnknown: editing a name that is not an embedded service is a
// usage error (exit 2) with a 'no embedded service' diagnostic; nothing written.
func TestCatalogEditUnknown(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", dir)
	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "edit", "nope"}, &out, &errb); code != agentsafety.ExitUsage {
		t.Fatalf("exit = %d, want %d (usage)", code, agentsafety.ExitUsage)
	}
	if !bytes.Contains(errb.Bytes(), []byte("no embedded service")) {
		t.Errorf("stderr = %q, want a 'no embedded service' diagnostic", errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "services", "nope.yaml")); !os.IsNotExist(err) {
		t.Errorf("an unknown-service edit should write nothing")
	}
}

// TestCatalogEditRejectsUnsafeNames: a name that is not a single safe path
// segment is rejected (exit usage) before any path is built — `catalog edit`
// must not let "../../etc/passwd" escape the config dir. Nothing is written
// anywhere, inside or outside the temp config dir.
func TestCatalogEditRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"..", "../../etc/passwd", "a/b", "", ".", "/etc/passwd", "foo/../bar"} {
		t.Run(name, func(t *testing.T) {
			cfg := t.TempDir()
			outside := t.TempDir() // a sibling dir a traversal might try to reach
			t.Setenv("LABCTL_CONFIG_DIR", cfg)

			var out, errb bytes.Buffer
			if code := Run([]string{"catalog", "edit", name}, &out, &errb); code != agentsafety.ExitUsage {
				t.Fatalf("exit = %d, want %d (usage) for name %q", code, agentsafety.ExitUsage, name)
			}
			assertDirEmpty(t, filepath.Join(cfg, "services"))
			assertDirEmpty(t, outside)
		})
	}
}

// TestCatalogVendorRejectsUnsafeNames: same traversal guard on `catalog vendor` —
// the name feeds both the override read path and the catalog/ write path.
func TestCatalogVendorRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"..", "../../etc/passwd", "a/b", "", ".", "/etc/passwd", "foo/../bar"} {
		t.Run(name, func(t *testing.T) {
			cfg := t.TempDir()
			t.Setenv("LABCTL_CONFIG_DIR", cfg)
			catDir := t.TempDir()

			var out, errb bytes.Buffer
			if code := Run([]string{"catalog", "vendor", name, "--catalog-dir", catDir}, &out, &errb); code != agentsafety.ExitUsage {
				t.Fatalf("exit = %d, want %d (usage) for name %q", code, agentsafety.ExitUsage, name)
			}
			assertDirEmpty(t, catDir)
		})
	}
}

// assertDirEmpty fails if dir exists and contains any entry (a missing dir counts
// as empty — the guard should prevent the dir from ever being created).
func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("read %s: %v", dir, err)
	}
	if len(entries) != 0 {
		t.Errorf("expected %s to be empty, found %d entr(ies): %v", dir, len(entries), entries)
	}
}

// TestCatalogVendorRoundTrip walks the full authoring loop: edit seeds the
// override, vendor validates it and writes catalog/<name>.yaml under --catalog-dir,
// byte-for-byte matching the override, and prints its absolute path.
func TestCatalogVendorRoundTrip(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)
	catDir := t.TempDir()

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "edit", "authentik"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("edit exit = %d, want 0 (stderr: %s)", code, errb.String())
	}

	out.Reset()
	errb.Reset()
	if code := Run([]string{"catalog", "vendor", "authentik", "--catalog-dir", catDir}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("vendor exit = %d, want 0 (stderr: %s)", code, errb.String())
	}

	dest := filepath.Join(catDir, "authentik.yaml")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("vendored file not written: %v", err)
	}
	src, _ := os.ReadFile(filepath.Join(cfg, "services", "authentik.yaml"))
	if !bytes.Equal(got, src) {
		t.Errorf("vendored file does not match the override source")
	}
	absDest, _ := filepath.Abs(dest)
	if gotPath := bytes.TrimSpace(out.Bytes()); string(gotPath) != absDest {
		t.Errorf("stdout = %q, want the absolute path %q", gotPath, absDest)
	}
}

// TestCatalogVendorMissingOverride: vendoring with no local override for the name
// is a usage error (exit 2) that points the user at `catalog edit`.
func TestCatalogVendorMissingOverride(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	catDir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "vendor", "authentik", "--catalog-dir", catDir}, &out, &errb); code != agentsafety.ExitUsage {
		t.Fatalf("exit = %d, want %d (usage)", code, agentsafety.ExitUsage)
	}
	if !bytes.Contains(errb.Bytes(), []byte("no local override")) {
		t.Errorf("stderr = %q, want a 'no local override' diagnostic", errb.String())
	}
	if _, err := os.Stat(filepath.Join(catDir, "authentik.yaml")); !os.IsNotExist(err) {
		t.Errorf("vendor should write nothing when the override is missing")
	}
}

// TestCatalogVendorValidatesBeforeWriting: a structurally broken override (a
// portable manifest may not carry a base_url) is rejected and never promoted.
func TestCatalogVendorValidatesBeforeWriting(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)
	catDir := t.TempDir()
	// An in-manifest base_url makes the manifest non-portable — structural
	// Validate rejects it, so vendor must refuse it.
	const broken = `name: authentik
base_url: https://auth.example
auth:
  strategy: none
commands:
  list:
    method: GET
    path: /api/v3/core/users/
`
	writeService(t, cfg, "authentik", broken)

	var out, errb bytes.Buffer
	code := Run([]string{"catalog", "vendor", "authentik", "--catalog-dir", catDir}, &out, &errb)
	if code == agentsafety.ExitOK {
		t.Fatalf("vendor of a broken override succeeded, want failure (stderr: %s)", errb.String())
	}
	if _, err := os.Stat(filepath.Join(catDir, "authentik.yaml")); !os.IsNotExist(err) {
		t.Errorf("a broken override must not be promoted into catalog/")
	}
}

// TestCatalogVendorRefusesClobber: an existing catalog/<name>.yaml is not
// overwritten without --force; with --force it is replaced.
func TestCatalogVendorRefusesClobber(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)
	catDir := t.TempDir()

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "edit", "authentik"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("edit exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	dest := filepath.Join(catDir, "authentik.yaml")
	const sentinel = "name: authentik\n# already vendored\n"
	if err := os.WriteFile(dest, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	// Without --force: usage error, sentinel untouched.
	out.Reset()
	errb.Reset()
	if code := Run([]string{"catalog", "vendor", "authentik", "--catalog-dir", catDir}, &out, &errb); code != agentsafety.ExitUsage {
		t.Fatalf("exit = %d, want %d (usage) (stderr: %s)", code, agentsafety.ExitUsage, errb.String())
	}
	if got, _ := os.ReadFile(dest); string(got) != sentinel {
		t.Errorf("catalog file was clobbered without --force: %q", got)
	}

	// With --force: replaced by the override.
	out.Reset()
	errb.Reset()
	if code := Run([]string{"catalog", "vendor", "authentik", "--catalog-dir", catDir, "--force"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("--force exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	got, _ := os.ReadFile(dest)
	src, _ := os.ReadFile(filepath.Join(cfg, "services", "authentik.yaml"))
	if !bytes.Equal(got, src) {
		t.Errorf("--force did not replace the catalog file with the override")
	}
}
