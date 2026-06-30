package catalog

import (
	"bytes"
	"testing"
)

// TestNamesAndManifest confirms the embedded index is non-empty, sorted, and that
// every listed name resolves to non-empty bytes (round-trip Names → Manifest).
func TestNamesAndManifest(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Fatal("embedded catalog is empty")
	}
	for i, n := range names {
		if i > 0 && names[i-1] > n {
			t.Fatalf("Names not sorted: %q before %q", names[i-1], n)
		}
		data, ok := Manifest(n)
		if !ok {
			t.Errorf("Manifest(%q) missing for a listed name", n)
		}
		if len(bytes.TrimSpace(data)) == 0 {
			t.Errorf("Manifest(%q) is empty", n)
		}
	}
}

// TestManifestUnknown confirms an unknown name reports not-found.
func TestManifestUnknown(t *testing.T) {
	if _, ok := Manifest("definitely-not-a-service"); ok {
		t.Fatal("unknown service should not resolve")
	}
}
