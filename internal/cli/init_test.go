package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedwards1230/labctl/internal/agentsafety"
	"github.com/jedwards1230/labctl/internal/manifest"
)

// TestInitStdout confirms `labctl init <svc>` prints a validating manifest to
// stdout by default.
func TestInitStdout(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := Run([]string{"init", "demo"}, &out, &errb); code != agentsafety.ExitOK {
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
	if code := Run([]string{"init", "demo", "-o", path}, &out, &errb); code != agentsafety.ExitOK {
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
	if code := Run([]string{"init", "demo", "-o", path}, &out, &errb); code != agentsafety.ExitUsage {
		t.Fatalf("overwrite without --force exit = %d, want %d", code, agentsafety.ExitUsage)
	}
	if !strings.Contains(errb.String(), "already exists") {
		t.Errorf("expected 'already exists' diagnostic, got: %s", errb.String())
	}

	// With --force it overwrites cleanly.
	out.Reset()
	errb.Reset()
	if code := Run([]string{"init", "demo", "-o", path, "--force"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("overwrite with --force exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
}

// TestInitUnknownAuth confirms a bad --auth scheme is a usage error.
func TestInitUnknownAuth(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := Run([]string{"init", "demo", "--auth", "nope"}, &out, &errb); code != agentsafety.ExitUsage {
		t.Fatalf("bad auth exit = %d, want %d (stderr: %s)", code, agentsafety.ExitUsage, errb.String())
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
			if code := Run([]string{"init", "demo", "--auth", auth, "-o", path, "--force"}, &out, &errb); code != agentsafety.ExitOK {
				t.Fatalf("init exit = %d (stderr: %s)", code, errb.String())
			}
			out.Reset()
			errb.Reset()
			if code := Run([]string{"lint", path}, &out, &errb); code != agentsafety.ExitOK {
				t.Fatalf("lint exit = %d, want 0 (stderr: %s)", code, errb.String())
			}
		})
	}
}

// TestInitProvisionsConfigDir confirms bare `labctl init` provisions the config
// dir (config.yaml + services/ + profile.yaml) and is idempotent — a second run
// clobbers nothing and still exits 0.
func TestInitProvisionsConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"init"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("init exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	for _, p := range []string{"config.yaml", "profile.yaml", "services"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("init did not provision %s: %v", p, err)
		}
	}
	if !strings.Contains(errb.String(), "created:") {
		t.Errorf("init should report created actions on stderr:\n%s", errb.String())
	}

	// Mutate config.yaml, then re-run: the second run must NOT clobber it.
	cfgPath := filepath.Join(dir, "config.yaml")
	const sentinel = "# user edited\n"
	if err := os.WriteFile(cfgPath, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{"init"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("second init exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != sentinel {
		t.Errorf("idempotent init clobbered config.yaml:\n%s", b)
	}
	if !strings.Contains(errb.String(), "left as-is") {
		t.Errorf("second init should report existing files left as-is:\n%s", errb.String())
	}

	// The provisioned config.yaml + profile.yaml must load clean (locks the
	// seeded defaults against future strict-decode drift). Restore the default
	// config.yaml first, since this test mutated it above.
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{"init"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("re-provision exit = %d (stderr: %s)", code, errb.String())
	}
	if _, err := manifest.Load(dir); err != nil {
		t.Fatalf("provisioned config dir should load cleanly: %v", err)
	}
}

// TestLintStrictPortable confirms a portable manifest passes plain `lint` (exit
// 0) but fails `lint --strict` (exit 2), which enforces completeness.
func TestLintStrictPortable(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfgDir)
	// A portable manifest file: no base_url, an unbound secret slot.
	path := filepath.Join(cfgDir, "portable.yaml")
	const portable = `name: portable
env_prefix: PORTABLE
auth:
  strategy: header-key
  header: X-Api-Key
  value: "{secret.api_key}"
secrets:
  api_key:
    env: PORTABLE_API_KEY
commands:
  status: { method: GET, path: /api/status }
`
	if err := os.WriteFile(path, []byte(portable), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := Run([]string{"lint", path}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("plain lint exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{"lint", "--strict", path}, &out, &errb); code != agentsafety.ExitUsage {
		t.Fatalf("strict lint exit = %d, want %d (stderr: %s)", code, agentsafety.ExitUsage, errb.String())
	}
	if !strings.Contains(errb.String(), "unbound") && !strings.Contains(errb.String(), "base_url") {
		t.Errorf("strict lint should explain the incompleteness:\n%s", errb.String())
	}
}

// TestDoctorReportsIncomplete confirms doctor reports `incomplete:` for an
// unbound portable service and continues to the next service without aborting.
func TestDoctorReportsIncomplete(t *testing.T) {
	dir := t.TempDir()
	// One portable-but-unbound service, one complete service.
	writeService(t, dir, "portable", `name: portable
auth: { strategy: none }
commands:
  status: { method: GET, path: /s }
`)
	writeService(t, dir, "complete", `name: complete
auth: { strategy: none }
commands:
  status: { method: GET, path: /s }
`)
	// Bind only the "complete" service — "portable" is intentionally left unbound.
	writeProfile(t, dir, `version: 1
services:
  complete:
    base_url: http://127.0.0.1:1
`)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"doctor"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("doctor exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, "portable") || !strings.Contains(got, "incomplete:") {
		t.Errorf("doctor should report the portable service as incomplete:\n%s", got)
	}
	// It must not abort — the complete service is still listed (unreachable here).
	if !strings.Contains(got, "complete") {
		t.Errorf("doctor should continue to the complete service:\n%s", got)
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
	manifestYAML := []byte("name: radarr\nauth:\n  strategy: none\ncommands:\n  list:\n    method: GET\n    path: /m\n")
	if err := os.WriteFile(filepath.Join(svcDir, "radarr.yaml"), manifestYAML, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LABCTL_CONFIG_DIR", dir)
	var out, errb bytes.Buffer
	if code := Run([]string{"mcp", "--service", "nonexistent"}, &out, &errb); code != agentsafety.ExitUsage {
		t.Fatalf("mcp unknown service exit = %d, want %d (stderr: %s)", code, agentsafety.ExitUsage, errb.String())
	}
	if !strings.Contains(errb.String(), "nonexistent") || !strings.Contains(errb.String(), "radarr") {
		t.Errorf("error should name the unknown service and list available; got: %s", errb.String())
	}
}
