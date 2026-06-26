package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitStdout confirms `labctl init <svc>` prints a validating manifest to
// stdout by default.
func TestInitStdout(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := Run([]string{"init", "demo"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if !strings.Contains(out.String(), "name: demo") {
		t.Errorf("init stdout missing 'name: demo':\n%s", out.String())
	}
	if !strings.Contains(out.String(), "strategy: header-key") {
		t.Errorf("default init should emit header-key auth:\n%s", out.String())
	}
}

// TestInitOutputFileAndForce confirms -o writes a file, refuses to clobber, and
// honors --force.
func TestInitOutputFileAndForce(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "demo.yaml")

	var out, errb bytes.Buffer
	if code := Run([]string{"init", "demo", "-o", path}, &out, &errb); code != exitOK {
		t.Fatalf("write exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if !strings.Contains(string(b), "name: demo") {
		t.Errorf("written file missing 'name: demo':\n%s", b)
	}

	// A second write without --force must refuse (exit 2).
	out.Reset()
	errb.Reset()
	if code := Run([]string{"init", "demo", "-o", path}, &out, &errb); code != exitUsage {
		t.Fatalf("overwrite without --force exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errb.String(), "already exists") {
		t.Errorf("expected 'already exists' diagnostic, got: %s", errb.String())
	}

	// With --force it overwrites cleanly.
	out.Reset()
	errb.Reset()
	if code := Run([]string{"init", "demo", "-o", path, "--force"}, &out, &errb); code != exitOK {
		t.Fatalf("overwrite with --force exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
}

// TestInitUnknownAuth confirms a bad --auth scheme is a usage error.
func TestInitUnknownAuth(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := Run([]string{"init", "demo", "--auth", "nope"}, &out, &errb); code != exitUsage {
		t.Fatalf("bad auth exit = %d, want %d (stderr: %s)", code, exitUsage, errb.String())
	}
}

// TestInitOutputValidatesViaLint round-trips: init writes a manifest, then lint
// validates it — proving scaffolds pass the lint gate end-to-end.
func TestInitOutputValidatesViaLint(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfgDir)
	path := filepath.Join(cfgDir, "scaffold.yaml")
	for _, auth := range []string{"none", "bearer", "ws-login"} {
		t.Run(auth, func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := Run([]string{"init", "demo", "--auth", auth, "-o", path, "--force"}, &out, &errb); code != exitOK {
				t.Fatalf("init exit = %d (stderr: %s)", code, errb.String())
			}
			out.Reset()
			errb.Reset()
			if code := Run([]string{"lint", path}, &out, &errb); code != exitOK {
				t.Fatalf("lint exit = %d, want 0 (stderr: %s)", code, errb.String())
			}
		})
	}
}

// TestMCPUnknownServiceErrors confirms `labctl mcp --service <unknown>` fails
// fast with a usage error rather than serving an empty tool set.
func TestMCPUnknownServiceErrors(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "services")
	if err := os.MkdirAll(svcDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifestYAML := []byte("name: radarr\nbase_url: http://localhost\nauth:\n  strategy: none\ncommands:\n  list:\n    method: GET\n    path: /m\n")
	if err := os.WriteFile(filepath.Join(svcDir, "radarr.yaml"), manifestYAML, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LABCTL_CONFIG_DIR", dir)
	var out, errb bytes.Buffer
	if code := Run([]string{"mcp", "--service", "nonexistent"}, &out, &errb); code != exitUsage {
		t.Fatalf("mcp unknown service exit = %d, want %d (stderr: %s)", code, exitUsage, errb.String())
	}
	if !strings.Contains(errb.String(), "nonexistent") || !strings.Contains(errb.String(), "radarr") {
		t.Errorf("error should name the unknown service and list available; got: %s", errb.String())
	}
}
