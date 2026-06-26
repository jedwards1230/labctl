package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

// examplesDir resolves the repo's examples/ config dir relative to this test
// file's package directory (internal/manifest → ../../examples).
func examplesDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "examples"))
	if err != nil {
		t.Fatalf("resolve examples dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "services")); err != nil {
		t.Fatalf("examples/services not found at %s: %v", dir, err)
	}
	return dir
}

// TestExamplesLoadAndValidate turns the shipped examples/ set into a living
// contract: every service manifest must load, validate structurally, and the
// merged global config must pass ValidateConfig. This catches a malformed or
// schema-drifted example before it reaches a user's config dir.
//
// It performs no network calls and resolves no secrets — Load is purely
// structural (YAML parse + Validate + ValidateConfig + offline spec inference).
func TestExamplesLoadAndValidate(t *testing.T) {
	dir := examplesDir(t)

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load(%s): %v", dir, err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil Loaded")
	}

	// The loaded config itself must validate (Load already checks this, but the
	// example set is a contract — assert it explicitly so a future change that
	// loosens Load can't silently ship an invalid example config).
	if err := ValidateConfig(&loaded.Config); err != nil {
		t.Fatalf("ValidateConfig(examples/config.yaml): %v", err)
	}

	// Cross-check the loaded service count against the YAML files on disk so an
	// example that silently fails to register (wrong extension, dir entry) is
	// caught rather than skipped.
	wantNames := exampleServiceNames(t, dir)
	if len(loaded.Services) != len(wantNames) {
		t.Fatalf("loaded %d services, want %d (from %v)", len(loaded.Services), len(wantNames), wantNames)
	}

	for _, name := range wantNames {
		svc, ok := loaded.Services[name]
		if !ok {
			t.Errorf("example service %q did not register", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			if err := Validate(svc); err != nil {
				t.Fatalf("Validate(%s): %v", name, err)
			}
		})
	}
}

// exampleServiceNames returns the service selector names implied by the *.yaml /
// *.yml files under examples/services (filename stem, matching Load's default).
func exampleServiceNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "services"))
	if err != nil {
		t.Fatalf("read examples/services: %v", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !isYAML(e.Name()) {
			continue
		}
		stem := e.Name()
		stem = stem[:len(stem)-len(filepath.Ext(stem))]
		names = append(names, stem)
	}
	if len(names) == 0 {
		t.Fatal("no example service manifests found")
	}
	return names
}
