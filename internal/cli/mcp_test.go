package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jedwards1230/labctl/internal/agentsafety"
)

// ── resolveHTTPAuth (pure, no listener) ────────────────────────────────────
//
// This is the same policy mcpserver.RequireAuth exercises directly, tested
// again here to pin the CLI-layer wiring (token-file resolution feeding into
// the guard) without ever starting a real HTTP server.

func TestResolveHTTPAuth(t *testing.T) {
	cases := []struct {
		name                 string
		addr                 string
		authTokenFile        string
		allowUnauthenticated bool
		env                  string
		wantErr              bool
	}{
		{
			name:    "non-loopback, no token, no opt-out -> refused",
			addr:    ":9000",
			wantErr: true,
		},
		{
			name:                 "non-loopback, no token, opt-out -> starts",
			addr:                 ":9000",
			allowUnauthenticated: true,
			wantErr:              false,
		},
		{
			name:    "non-loopback, env token configured -> starts",
			addr:    ":9000",
			env:     "a-real-token",
			wantErr: false,
		},
		{
			name:    "loopback, no token -> starts (unchanged behavior)",
			addr:    "127.0.0.1:9000",
			wantErr: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("LABCTL_MCP_AUTH_TOKEN", tc.env)
			} else {
				t.Setenv("LABCTL_MCP_AUTH_TOKEN", "")
			}
			_, err := resolveHTTPAuth(tc.addr, tc.authTokenFile, tc.allowUnauthenticated)
			if tc.wantErr && err == nil {
				t.Fatalf("resolveHTTPAuth(%q, %q, %v) = nil, want an error", tc.addr, tc.authTokenFile, tc.allowUnauthenticated)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("resolveHTTPAuth(%q, %q, %v) = %v, want nil", tc.addr, tc.authTokenFile, tc.allowUnauthenticated, err)
			}
		})
	}
}

// TestResolveHTTPAuth_BadTokenFilePropagates ensures a bad --auth-token-file
// (missing/empty) surfaces as an error before RequireAuth's policy check even
// runs — fail closed on operator misconfiguration.
func TestResolveHTTPAuth_BadTokenFilePropagates(t *testing.T) {
	_, err := resolveHTTPAuth("127.0.0.1:9000", "/no/such/token/file", true)
	if err == nil {
		t.Fatal("expected an error for a missing --auth-token-file")
	}
}

// ── cmdMCP wiring through Run() ─────────────────────────────────────────────

// TestMCPHTTPRefusesUnauthenticatedNonLoopback verifies the end-to-end CLI
// path: `mcp --http <non-loopback>` with no token and no opt-out refuses to
// start with a usage error (exit 2) — and, importantly, returns before ever
// opening a listener.
func TestMCPHTTPRefusesUnauthenticatedNonLoopback(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "radarr", validManifestBody)
	t.Setenv("LABCTL_CONFIG_DIR", dir)
	t.Setenv("LABCTL_MCP_AUTH_TOKEN", "")

	var out, errb bytes.Buffer
	code := Run([]string{"mcp", "--http", ":9000"}, &out, &errb)
	if code != agentsafety.ExitUsage {
		t.Fatalf("exit = %d, want %d (usage); stderr: %s", code, agentsafety.ExitUsage, errb.String())
	}
	msg := errb.String()
	for _, want := range []string{"LABCTL_MCP_AUTH_TOKEN", "--auth-token-file", "--allow-unauthenticated"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr = %q, want it to mention %q", msg, want)
		}
	}
}

// TestMCPAllowUnauthenticatedRequiresHTTP verifies --allow-unauthenticated
// without --http is rejected as a usage error (mirrors the existing
// --auth-token-file-without---http guard) — no listener is ever attempted.
func TestMCPAllowUnauthenticatedRequiresHTTP(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "radarr", validManifestBody)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	code := Run([]string{"mcp", "--allow-unauthenticated"}, &out, &errb)
	if code != agentsafety.ExitUsage {
		t.Fatalf("exit = %d, want %d (usage); stderr: %s", code, agentsafety.ExitUsage, errb.String())
	}
	if !strings.Contains(errb.String(), "--allow-unauthenticated") {
		t.Fatalf("stderr = %q, want it to mention --allow-unauthenticated", errb.String())
	}
}

// TestMCPHelpDocumentsSecureDefault checks `mcp --help` documents the new
// secure-by-default behavior and the opt-out flag.
func TestMCPHelpDocumentsSecureDefault(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "radarr", validManifestBody)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"mcp", "--help"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("--help exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	help := out.String()
	for _, want := range []string{"--allow-unauthenticated", "non-loopback", "REFUSES"} {
		if !strings.Contains(help, want) {
			t.Errorf("mcp --help = %q, want it to mention %q", help, want)
		}
	}
}
