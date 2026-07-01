package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jedwards1230/labctl/catalog"
)

// examplesDir resolves the repo's examples/ config dir relative to this test
// file's package directory (internal/manifest → ../../examples).
func examplesDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "examples"))
	if err != nil {
		t.Fatalf("resolve examples dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "profile.yaml")); err != nil {
		t.Fatalf("examples/profile.yaml not found at %s: %v", dir, err)
	}
	return dir
}

// TestExamplesLoadAndValidate turns the shipped examples/ config into a living
// contract. examples/ carries NO services/ dir — it is profile-only — so every
// service comes from the embedded catalog and examples/profile.yaml binds it.
// This proves a consumer can drop their vendored manifests entirely: the catalog
// plus a profile is a complete, working config.
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

	// examples/ ships no local services, so every service is the embedded catalog.
	want := catalog.Names()
	if len(loaded.Services) != len(want) {
		t.Fatalf("loaded %d services, want %d embedded (%v)", len(loaded.Services), len(want), want)
	}

	for _, name := range want {
		svc, ok := loaded.Services[name]
		if !ok {
			t.Errorf("embedded service %q did not register", name)
			continue
		}
		if got := loaded.OriginOf(name); got != OriginEmbedded {
			t.Errorf("%s origin = %q, want embedded (examples ships no local overrides)", name, got)
		}
		t.Run(name, func(t *testing.T) {
			// Structural Validate already ran on the RAW manifest during Load; it
			// cannot be re-run on `svc` here because the loaded service has been
			// profile-merged and now carries base_url/refs, which the structural
			// "no in-manifest binding" rule forbids. Load applies
			// examples/profile.yaml, so the embedded catalog must also be COMPLETE
			// through it: every catalog manifest is portable (no base_url or secret
			// ref) and bound to its endpoint and credentials via
			// examples/profile.yaml. This proves the catalog+profile are a working
			// end-to-end config.
			if err := ValidateComplete(svc); err != nil {
				t.Fatalf("ValidateComplete(%s): %v", name, err)
			}
		})
	}
}
