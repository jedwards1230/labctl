package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunVersion exercises the full Run wiring (telemetry start → cobra tree →
// builtin) for a known-good path and asserts a clean exit.
func TestRunVersion(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := Run([]string{"version"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if !strings.Contains(out.String(), "labctl") {
		t.Errorf("version stdout = %q, want to contain 'labctl'", out.String())
	}
}

// TestRunListEmptyConfig confirms an empty config dir is not an error — `list`
// reports no services and exits 0.
func TestRunListEmptyConfig(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := Run([]string{"list"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if !strings.Contains(out.String(), "No services configured") {
		t.Errorf("list stdout = %q, want 'No services configured'", out.String())
	}
}

// TestRunUnknownCommand confirms an unrecognized subcommand exits with exitUsage (2).
func TestRunUnknownCommand(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := Run([]string{"definitely-not-a-service"}, &out, &errb); code != exitUsage {
		t.Errorf("unknown command exit = %d, want %d (stderr: %s)", code, exitUsage, errb.String())
	}
}

// TestRunUnknownServiceExits2 confirms labctl <unknown-service> exits exitUsage.
func TestRunUnknownServiceExits2(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := Run([]string{"bogus-service"}, &out, &errb); code != exitUsage {
		t.Fatalf("unknown service exit = %d, want %d (stderr: %s)", code, exitUsage, errb.String())
	}
}

// TestRunUnknownSubcommandExits2 confirms labctl <service> <unknown-cmd> exits exitUsage.
// Manifests live under <configDir>/services/, so the radarr.yaml is placed there
// to ensure "radarr" is actually registered as a service command before we ask for
// a non-existent subcommand.
func TestRunUnknownSubcommandExits2(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "services")
	if err := os.MkdirAll(svcDir, 0700); err != nil {
		t.Fatal(err)
	}
	svcManifest := []byte(`
name: radarr
base_url: http://localhost
auth:
  strategy: none
commands:
  list:
    method: GET
    path: /api/v3/movie
`)
	if err := os.WriteFile(filepath.Join(svcDir, "radarr.yaml"), svcManifest, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LABCTL_CONFIG_DIR", dir)
	var out, errb bytes.Buffer
	if code := Run([]string{"radarr", "bogus-cmd"}, &out, &errb); code != exitUsage {
		t.Fatalf("unknown subcommand exit = %d, want %d (stderr: %s)", code, exitUsage, errb.String())
	}
}
