package cli

import (
	"bytes"
	"strings"
	"testing"
)

// svcManifest is a minimal no-auth radarr manifest used by the svc-namespace
// tests. base_url points at a non-routable host; every test uses --dry-run so
// nothing is ever sent.
const svcManifest = `
name: radarr
description: movie manager
base_url: http://movies.example
auth:
  strategy: none
commands:
  list:
    method: GET
    path: /api/v3/movie
`

// TestSvcResolvesServiceCommand confirms a service command resolves under the
// `svc` parent: `labctl svc radarr list --dry-run` prints the resolved request
// and exits 0 (no network, no secrets).
func TestSvcResolvesServiceCommand(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "radarr", svcManifest)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"svc", "radarr", "list", "--dry-run"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if !strings.Contains(out.String(), "GET http://movies.example/api/v3/movie") {
		t.Fatalf("svc dry-run stdout = %q, want the resolved request line", out.String())
	}
}

// TestSvcAliasResolves confirms the `s` alias for `svc` routes the same way.
func TestSvcAliasResolves(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "radarr", svcManifest)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"s", "radarr", "list", "--dry-run"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if !strings.Contains(out.String(), "GET http://movies.example/api/v3/movie") {
		t.Fatalf("alias dry-run stdout = %q, want the resolved request line", out.String())
	}
}

// TestSvcBareListsServices confirms bare `labctl svc` lists the configured
// services (same content as `labctl list`) and does not error.
func TestSvcBareListsServices(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "radarr", svcManifest)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"svc"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, "radarr") || !strings.Contains(got, "movie manager") {
		t.Fatalf("bare svc output = %q, want name + description (parity with `list`)", got)
	}
}

// TestSvcBareEmptyConfigGraceful confirms bare `labctl svc` with no configured
// services does not crash — it reports "No services configured" and exits 0.
func TestSvcBareEmptyConfigGraceful(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := Run([]string{"svc"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if !strings.Contains(out.String(), "No services configured") {
		t.Fatalf("bare svc stdout = %q, want 'No services configured'", out.String())
	}
}

// TestServiceNotRegisteredAtRoot is the core guarantee of this refactor: a
// service name is NOT a top-level command. `labctl radarr list` is an unknown
// command (exit 2), and root --help advertises `svc` but never the service name.
func TestServiceNotRegisteredAtRoot(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "radarr", svcManifest)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	// A bare service invocation at the root is now an unknown command.
	var out, errb bytes.Buffer
	if code := Run([]string{"radarr", "list"}, &out, &errb); code != exitUsage {
		t.Fatalf("root service invocation exit = %d, want %d (usage)", code, exitUsage)
	}

	// Root help lists `svc` but not the service itself.
	var hOut, hErr bytes.Buffer
	if code := Run([]string{"--help"}, &hOut, &hErr); code != exitOK {
		t.Fatalf("--help exit = %d, want 0 (stderr: %s)", code, hErr.String())
	}
	help := hOut.String()
	if !strings.Contains(help, "svc") {
		t.Fatalf("root help = %q, want it to advertise the `svc` command", help)
	}
	if strings.Contains(help, "radarr") {
		t.Fatalf("root help = %q, must NOT list service %q at the top level", help, "radarr")
	}
}

// TestRootBuiltinsStayAtRoot confirms the built-ins remain top-level commands
// after the refactor (they are listed in root --help).
func TestRootBuiltinsStayAtRoot(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := Run([]string{"--help"}, &out, &errb); code != exitOK {
		t.Fatalf("--help exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	help := out.String()
	for _, builtin := range []string{"init", "list", "lint", "doctor", "mcp", "version", "self-update", "svc"} {
		if !strings.Contains(help, builtin) {
			t.Fatalf("root help = %q, want top-level builtin %q", help, builtin)
		}
	}
}
