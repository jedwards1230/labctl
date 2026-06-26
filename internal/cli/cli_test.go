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

// TestRunConfigValidationErrorExits2 confirms a config.yaml validation failure
// (a service_account_token with two sources set) classifies to exit 2 and
// surfaces its real diagnostic — consistently across list, lint, and a service
// command — instead of exit 1 or a misleading "no manifests loaded".
func TestRunConfigValidationErrorExits2(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "services")
	if err := os.MkdirAll(svcDir, 0700); err != nil {
		t.Fatal(err)
	}
	// A valid service manifest exists, but the broken config.yaml fails the
	// whole load before any service registers.
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
	// service_account_token sets two sources (file + value) → exactly-one-of.
	configYAML := []byte(`
secrets:
  providers:
    onepassword:
      scheme: op
      auth:
        service_account_token:
          file: /tmp/token
          value: literal-token
`)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), configYAML, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	const wantDiag = "set exactly one of file|value|env (found 2)"
	cases := []struct {
		name string
		args []string
	}{
		{"list", []string{"list"}},
		{"lint", []string{"lint"}},
		{"service-command", []string{"radarr", "list"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := Run(tc.args, &out, &errb)
			if code != exitUsage {
				t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitUsage, errb.String())
			}
			if !strings.Contains(errb.String(), wantDiag) {
				t.Fatalf("stderr = %q, want to contain %q", errb.String(), wantDiag)
			}
			if strings.Contains(errb.String(), "no manifests loaded") {
				t.Fatalf("stderr surfaced misleading 'no manifests loaded': %q", errb.String())
			}
		})
	}
}
