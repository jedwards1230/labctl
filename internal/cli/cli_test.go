package cli

import (
	"bytes"
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

// TestRunUnknownCommand confirms an unrecognized subcommand exits non-zero.
func TestRunUnknownCommand(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := Run([]string{"definitely-not-a-service"}, &out, &errb); code == exitOK {
		t.Errorf("unknown command exit = 0, want non-zero")
	}
}
