package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

// exampleCatalogDir resolves the repo's examples/catalog/ reference catalog
// relative to this test file's package directory (internal/manifest →
// ../../examples/catalog). It is deliberately SINGULAR ("catalog", not
// "catalogs") and lives outside examples/ proper, so it is never picked up as
// an installed catalog by Load(examples) — see examples_lint_test.go's "15
// embedded services" contract.
func exampleCatalogDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "examples", "catalog"))
	if err != nil {
		t.Fatalf("resolve examples/catalog dir: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("examples/catalog not found at %s: %v", dir, err)
	}
	return dir
}

// TestExampleCatalogValidates is the reliable CI gate for the reference catalog
// under examples/catalog/: every *.yaml in it must pass ValidatePortableManifest
// — the exact gate `catalog add` and `catalog validate` enforce — so the example
// a third-party catalog author copies can never silently rot. It also asserts no
// two manifests share a service name, mirroring the duplicate-name check
// `catalog add`/`catalog validate` perform on a real source.
func TestExampleCatalogValidates(t *testing.T) {
	dir := exampleCatalogDir(t)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	seen := map[string]string{} // service name → file
	var manifestCount int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".yaml" && filepath.Ext(name) != ".yml" {
			continue
		}
		manifestCount++
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		t.Run(name, func(t *testing.T) {
			svcName, err := ValidatePortableManifest(b)
			if err != nil {
				t.Fatalf("ValidatePortableManifest(%s): %v", name, err)
			}
			if svcName == "" {
				t.Fatalf("%s: manifest has no name", name)
			}
			if prev, dup := seen[svcName]; dup {
				t.Fatalf("%s and %s both define service name %q", prev, name, svcName)
			}
			seen[svcName] = name
		})
	}
	if manifestCount == 0 {
		t.Fatalf("no manifests found in %s", dir)
	}
}
