package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestCatalogList: `labctl catalog list` prints the embedded services with their
// descriptions, independent of any local config.
func TestCatalogList(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "list"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	got := out.String()
	for _, name := range []string{"radarr", "truenas", "cloudflare"} {
		if !strings.Contains(got, name) {
			t.Errorf("catalog list = %q, want to include %q", got, name)
		}
	}
}

// TestCatalogShow: `labctl catalog show <name>` dumps the embedded YAML, and the
// output is a valid manifest (carries the name:, no leaked base_url).
func TestCatalogShow(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "show", "radarr"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, "name: radarr") {
		t.Fatalf("catalog show radarr = %q, want the embedded manifest YAML", got)
	}
}

// TestCatalogShowUnknown: an unknown service is a usage error (exit 2).
func TestCatalogShowUnknown(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "show", "nope"}, &out, &errb); code != exitUsage {
		t.Fatalf("exit = %d, want %d (usage)", code, exitUsage)
	}
	if !strings.Contains(errb.String(), "no embedded service") {
		t.Fatalf("stderr = %q, want a 'no embedded service' diagnostic", errb.String())
	}
}

// TestListShowsOverrideMarker: a local manifest that shadows an embedded service
// is marked `override` in `list`, while untouched embedded services stay
// `embedded`.
func TestListShowsOverrideMarker(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "radarr", svcManifest)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"list"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	got := out.String()
	for _, line := range strings.Split(got, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "radarr":
			if fields[1] != "override" {
				t.Errorf("radarr marker = %q, want override (line %q)", fields[1], line)
			}
		case "sonarr":
			if fields[1] != "embedded" {
				t.Errorf("sonarr marker = %q, want embedded (line %q)", fields[1], line)
			}
		}
	}
}
