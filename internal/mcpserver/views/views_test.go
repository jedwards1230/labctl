package views

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResultHTMLEmbeddedDefault proves the embedded copy is served when
// LABCTL_VIEWS_DIR is unset, and that it is non-empty, well-formed-enough
// stub HTML.
func TestResultHTMLEmbeddedDefault(t *testing.T) {
	t.Setenv("LABCTL_VIEWS_DIR", "")
	got := string(ResultHTML())
	if got == "" {
		t.Fatal("ResultHTML() returned empty bytes")
	}
	if !strings.Contains(got, "<html") {
		t.Errorf("ResultHTML() = %q, want it to contain <html", got)
	}
}

// TestResultHTMLViewsDirOverride proves LABCTL_VIEWS_DIR, when set and
// readable, overrides the embedded copy — the dev loop for iterating on
// views/ without a Go rebuild.
func TestResultHTMLViewsDirOverride(t *testing.T) {
	dir := t.TempDir()
	want := "<!doctype html><html><body>override</body></html>"
	if err := os.WriteFile(filepath.Join(dir, "result.html"), []byte(want), 0o644); err != nil {
		t.Fatalf("write override result.html: %v", err)
	}
	t.Setenv("LABCTL_VIEWS_DIR", dir)

	got := string(ResultHTML())
	if got != want {
		t.Errorf("ResultHTML() = %q, want override %q", got, want)
	}
}

// TestResultHTMLViewsDirMissingFileFallsBack proves an unreadable override
// (dir set but no result.html in it) falls back to the embedded copy rather
// than erroring or serving an empty body.
func TestResultHTMLViewsDirMissingFileFallsBack(t *testing.T) {
	t.Setenv("LABCTL_VIEWS_DIR", t.TempDir()) // empty dir, no result.html
	got := string(ResultHTML())
	if got != string(embeddedResultHTML) {
		t.Errorf("ResultHTML() with missing override file = %q, want embedded fallback", got)
	}
}
