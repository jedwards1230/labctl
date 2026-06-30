package manifest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// portableManifest is a minimal valid portable manifest body for a named service.
func portableManifest(name string) string {
	return "name: " + name + "\n" +
		"description: test " + name + "\n" +
		"auth: { strategy: none }\n" +
		"commands:\n" +
		"  list: { method: GET, path: /list }\n"
}

// installDirCatalog stages a dir of manifests and installs it as a named catalog,
// returning the config dir. It is the dir-source half of the add flow without the
// CLI.
func installDirCatalog(t *testing.T, configDir, catalog string, manifests map[string]string) {
	t.Helper()
	files := map[string][]byte{}
	for fname, body := range manifests {
		files[fname] = []byte(body)
	}
	meta := CatalogMeta{Name: catalog, Source: "/some/dir", Type: "dir"}
	if err := InstallCatalog(configDir, meta, files, false); err != nil {
		t.Fatalf("InstallCatalog(%s): %v", catalog, err)
	}
}

// TestInstallCatalogAndLoadProvenance: an installed-catalog service shows up in
// Load with origin catalog:<name>, shadowing the embedded floor.
func TestInstallCatalogAndLoadProvenance(t *testing.T) {
	dir := t.TempDir()
	installDirCatalog(t, dir, "mycat", map[string]string{
		"widget.yaml": portableManifest("widget"),
	})

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := loaded.Services["widget"]; !ok {
		t.Fatal("installed-catalog service 'widget' did not load")
	}
	if got := loaded.OriginOf("widget"); got != catalogOrigin("mycat") {
		t.Errorf("widget origin = %q, want %q", got, catalogOrigin("mycat"))
	}
	if got := loaded.OriginOf("widget"); !got.IsCatalog() || got.CatalogName() != "mycat" {
		t.Errorf("origin helpers: IsCatalog=%v CatalogName=%q", got.IsCatalog(), got.CatalogName())
	}
	// The embedded floor still loads for everything the catalog didn't touch.
	if got := loaded.OriginOf("radarr"); got != OriginEmbedded {
		t.Errorf("radarr origin = %q, want embedded (floor intact)", got)
	}
}

// TestInstalledCatalogShadowsEmbedded: a catalog manifest of the same name as an
// embedded service shadows the embedded one (origin becomes catalog:<name>).
func TestInstalledCatalogShadowsEmbedded(t *testing.T) {
	dir := t.TempDir()
	installDirCatalog(t, dir, "fork", map[string]string{
		"radarr.yaml": portableManifest("radarr"),
	})
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.OriginOf("radarr"); got != catalogOrigin("fork") {
		t.Errorf("radarr origin = %q, want %q (catalog shadows embedded)", got, catalogOrigin("fork"))
	}
	if loaded.Services["radarr"].Description != "test radarr" {
		t.Errorf("radarr description = %q, want the catalog's manifest body", loaded.Services["radarr"].Description)
	}
}

// TestOrphanStagingDirIgnoredByLoad: an interrupted `catalog add` can leave a
// dot-prefixed .tmp-* staging dir behind. Load must ignore it — otherwise it
// would load as a phantom catalog and, worse, trip the cross-catalog collision
// check against the real catalog and brick every subsequent load.
func TestOrphanStagingDirIgnoredByLoad(t *testing.T) {
	dir := t.TempDir()
	installDirCatalog(t, dir, "mycat", map[string]string{
		"widget.yaml": portableManifest("widget"),
	})
	// Simulate a crashed install: a staging dir holding the same service name.
	orphan := filepath.Join(CatalogsDir(dir), ".tmp-mycat-abc123")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "widget.yaml"), []byte(portableManifest("widget")), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load must ignore the orphan staging dir, got: %v", err)
	}
	if got := loaded.OriginOf("widget"); got != catalogOrigin("mycat") {
		t.Errorf("widget origin = %q, want %q (orphan ignored, real catalog wins)", got, catalogOrigin("mycat"))
	}
}

// TestLocalOverridesInstalledCatalog: a local services/<name>.yaml shadows an
// installed-catalog service, with origin 'override'.
func TestLocalOverridesInstalledCatalog(t *testing.T) {
	dir := t.TempDir()
	installDirCatalog(t, dir, "mycat", map[string]string{
		"widget.yaml": portableManifest("widget"),
	})
	svcDir := filepath.Join(dir, "services")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const local = "name: widget\ndescription: local widget\nauth: { strategy: none }\ncommands:\n  list: { method: GET, path: /local }\n"
	if err := os.WriteFile(filepath.Join(svcDir, "widget.yaml"), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.OriginOf("widget"); got != OriginOverride {
		t.Errorf("widget origin = %q, want override (local shadows installed catalog)", got)
	}
	if loaded.Services["widget"].Description != "local widget" {
		t.Errorf("widget description = %q, want the local file's", loaded.Services["widget"].Description)
	}
}

// TestCrossCatalogCollisionFailsLoad: two installed catalogs defining the same
// service name is a hard error at load, naming both catalogs.
func TestCrossCatalogCollisionFailsLoad(t *testing.T) {
	dir := t.TempDir()
	installDirCatalog(t, dir, "acat", map[string]string{"widget.yaml": portableManifest("widget")})
	installDirCatalog(t, dir, "bcat", map[string]string{"widget.yaml": portableManifest("widget")})

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected a cross-catalog collision error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "acat") || !strings.Contains(msg, "bcat") {
		t.Errorf("collision error %q must name both catalogs (acat, bcat)", msg)
	}
}

// TestMalformedInstalledManifestFailsLoad: a non-portable (base_url-carrying)
// manifest installed into a catalog fails Load — the portability boundary is
// enforced at load, not just on add.
func TestMalformedInstalledManifestFailsLoad(t *testing.T) {
	dir := t.TempDir()
	// Write a bad manifest straight into the catalog dir (bypassing the add gate)
	// to prove the loader itself rejects it.
	catDir := filepath.Join(CatalogsDir(dir), "bad")
	if err := os.MkdirAll(catDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const bad = "name: evil\nbase_url: https://evil.example\nauth: { strategy: none }\n"
	if err := os.WriteFile(filepath.Join(catDir, "evil.yaml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("a base_url-carrying installed manifest must fail Load")
	}
}

// TestRemoveCatalogDropsServices: removing a catalog deletes the dir and its
// services disappear from the next Load.
func TestRemoveCatalogDropsServices(t *testing.T) {
	dir := t.TempDir()
	installDirCatalog(t, dir, "mycat", map[string]string{"widget.yaml": portableManifest("widget")})

	if err := RemoveCatalog(dir, "mycat"); err != nil {
		t.Fatalf("RemoveCatalog: %v", err)
	}
	if _, err := os.Stat(filepath.Join(CatalogsDir(dir), "mycat")); !os.IsNotExist(err) {
		t.Error("catalog dir should be gone after RemoveCatalog")
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load after remove: %v", err)
	}
	if _, ok := loaded.Services["widget"]; ok {
		t.Error("widget should be gone after the catalog is removed")
	}
}

// TestRemoveCatalogNotInstalled: removing a catalog that isn't installed is a
// *ConfigError (exit 2).
func TestRemoveCatalogNotInstalled(t *testing.T) {
	dir := t.TempDir()
	err := RemoveCatalog(dir, "nope")
	if err == nil {
		t.Fatal("expected an error removing a non-installed catalog")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Errorf("want *ConfigError (exit 2), got %T: %v", err, err)
	}
}

// TestValidateCatalogNameRejectsUnsafe: the name guard rejects traversal,
// separators, absolute paths, and uppercase.
func TestValidateCatalogNameRejectsUnsafe(t *testing.T) {
	bad := []string{"", ".", "..", "a/b", "../../etc", "/etc", "-x", "Foo", "a.b"}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			if err := ValidateCatalogName(name); err == nil {
				t.Errorf("ValidateCatalogName(%q) = nil, want rejection", name)
			}
		})
	}
	good := []string{"a", "mycat", "my-cat", "my_cat", "cat123"}
	for _, name := range good {
		t.Run(name, func(t *testing.T) {
			if err := ValidateCatalogName(name); err != nil {
				t.Errorf("ValidateCatalogName(%q) = %v, want nil", name, err)
			}
		})
	}
}

// TestInstallCatalogRejectsTraversalKey: a files-map key that escapes the catalog
// dir is rejected and nothing is written.
func TestInstallCatalogRejectsTraversalKey(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{"../evil.yaml": []byte(portableManifest("evil"))}
	err := InstallCatalog(dir, CatalogMeta{Name: "mycat", Type: "dir"}, files, false)
	if err == nil {
		t.Fatal("expected rejection of a traversal files-map key")
	}
	if _, statErr := os.Stat(filepath.Join(CatalogsDir(dir), "mycat")); !os.IsNotExist(statErr) {
		t.Error("no catalog dir should be created on a rejected install")
	}
	// And the sibling file must not have been written outside the catalog.
	if _, statErr := os.Stat(filepath.Join(dir, "evil.yaml")); !os.IsNotExist(statErr) {
		t.Error("a traversal key must not write outside the catalog dir")
	}
}

// TestInstallCatalogExistsWithoutForce: installing over an existing catalog fails
// without force and the original is untouched; with force it is replaced.
func TestInstallCatalogExistsWithoutForce(t *testing.T) {
	dir := t.TempDir()
	installDirCatalog(t, dir, "mycat", map[string]string{"a.yaml": portableManifest("a")})

	err := InstallCatalog(dir, CatalogMeta{Name: "mycat", Type: "dir"}, map[string][]byte{"b.yaml": []byte(portableManifest("b"))}, false)
	if err == nil {
		t.Fatal("expected an error installing over an existing catalog without --force")
	}
	// Original manifest intact.
	if _, statErr := os.Stat(filepath.Join(CatalogsDir(dir), "mycat", "a.yaml")); statErr != nil {
		t.Error("the original catalog must be untouched on a non-force collision")
	}

	// With force: replaced.
	if err := InstallCatalog(dir, CatalogMeta{Name: "mycat", Type: "dir"}, map[string][]byte{"b.yaml": []byte(portableManifest("b"))}, true); err != nil {
		t.Fatalf("force install: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(CatalogsDir(dir), "mycat", "a.yaml")); !os.IsNotExist(statErr) {
		t.Error("the old manifest should be gone after a force replace")
	}
	if _, statErr := os.Stat(filepath.Join(CatalogsDir(dir), "mycat", "b.yaml")); statErr != nil {
		t.Error("the new manifest should be present after a force replace")
	}
}

// TestInstalledCatalogsListing: listing reports installed catalogs sorted, and a
// subdir with no metadata still appears (minimal meta) so it can be removed.
func TestInstalledCatalogsListing(t *testing.T) {
	dir := t.TempDir()
	installDirCatalog(t, dir, "bcat", map[string]string{"b.yaml": portableManifest("b")})
	installDirCatalog(t, dir, "acat", map[string]string{"a.yaml": portableManifest("a")})
	// A meta-less dir (simulating a hand-placed or crash-truncated catalog).
	bare := filepath.Join(CatalogsDir(dir), "ccat")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}

	cats, err := InstalledCatalogs(dir)
	if err != nil {
		t.Fatalf("InstalledCatalogs: %v", err)
	}
	if len(cats) != 3 {
		t.Fatalf("got %d catalogs, want 3", len(cats))
	}
	want := []string{"acat", "bcat", "ccat"}
	for i, c := range cats {
		if c.Name != want[i] {
			t.Errorf("cats[%d].Name = %q, want %q (sorted)", i, c.Name, want[i])
		}
	}
}

// TestReadCatalogMeta round-trips the metadata written by InstallCatalog.
func TestReadCatalogMeta(t *testing.T) {
	dir := t.TempDir()
	meta := CatalogMeta{Name: "gitcat", Source: "https://example.test/repo.git", Type: "git", Ref: "v1", Commit: "abc123def456"}
	if err := InstallCatalog(dir, meta, map[string][]byte{"a.yaml": []byte(portableManifest("a"))}, false); err != nil {
		t.Fatal(err)
	}
	got, found, err := ReadCatalogMeta(dir, "gitcat")
	if err != nil || !found {
		t.Fatalf("ReadCatalogMeta: found=%v err=%v", found, err)
	}
	if got.Source != meta.Source || got.Type != "git" || got.Ref != "v1" || got.Commit != "abc123def456" {
		t.Errorf("round-tripped meta = %+v, want source/type/ref/commit from %+v", got, meta)
	}

	if _, found, _ := ReadCatalogMeta(dir, "missing"); found {
		t.Error("ReadCatalogMeta for a missing catalog should report found=false")
	}
}
