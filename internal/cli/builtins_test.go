package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedwards1230/labctl/internal/manifest"
)

// validManifestBody is a PORTABLE manifest (no base_url) — it passes plain
// `lint` and `list`, which only check structure. Binding lives in profile.yaml.
const validManifestBody = `
name: radarr
description: movie manager
auth:
  strategy: none
commands:
  list:
    method: GET
    path: /api/v3/movie
`

// TestLintValidService: `lint <name>` of a valid manifest prints "ok <name>" at
// exit 0.
func TestLintValidService(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "radarr", validManifestBody)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"lint", "radarr"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if !strings.Contains(out.String(), "ok radarr") {
		t.Fatalf("stdout = %q, want 'ok radarr'", out.String())
	}
}

// TestLintSchemaBroken: a manifest with an unknown auth strategy fails the load
// with a ConfigError → exit 2 and a diagnostic on stderr.
func TestLintSchemaBroken(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "broken", `
name: broken
auth:
  strategy: not-a-real-strategy
commands:
  list:
    method: GET
    path: /x
`)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	code := Run([]string{"lint", "broken"}, &out, &errb)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d (usage/config)", code, exitUsage)
	}
	if !strings.Contains(errb.String(), "strategy") {
		t.Fatalf("stderr = %q, want a diagnostic mentioning the bad strategy", errb.String())
	}
}

// TestLintFilePath: `lint <path.yaml>` validates the file directly.
func TestLintFilePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "standalone.yaml")
	if err := os.WriteFile(path, []byte(validManifestBody), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir()) // empty config dir

	var out, errb bytes.Buffer
	if code := Run([]string{"lint", path}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if !strings.Contains(out.String(), "ok "+path) {
		t.Fatalf("stdout = %q, want 'ok %s'", out.String(), path)
	}
}

// TestLintUnknownService: `lint <unknown>` is a usage error (exit 2).
func TestLintUnknownService(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "radarr", validManifestBody)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"lint", "nope"}, &out, &errb); code != exitUsage {
		t.Fatalf("exit = %d, want %d (usage)", code, exitUsage)
	}
}

// TestListDescriptions: `list` prints name + description columns.
func TestListDescriptions(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "radarr", validManifestBody)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"list"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, "radarr") || !strings.Contains(got, "movie manager") {
		t.Fatalf("list output = %q, want name + description", got)
	}
}

// TestProbeSkip covers every skip case of the pure skip classifier.
func TestProbeSkip(t *testing.T) {
	cases := []struct {
		name string
		svc  *manifest.Service
		skip bool
	}{
		{"empty base", &manifest.Service{}, true},
		{"templated base", &manifest.Service{BaseURL: "https://{host}:8080"}, true},
		{"wss base", &manifest.Service{BaseURL: "wss://x/ws"}, true},
		{"jsonrpc-ws transport", &manifest.Service{BaseURL: "http://x", Transport: "jsonrpc-ws"}, true},
		{"plain http", &manifest.Service{BaseURL: "http://x"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, skip := probeSkip(c.svc)
			if skip != c.skip {
				t.Fatalf("probeSkip skip = %v, want %v (reason %q)", skip, c.skip, reason)
			}
			if skip && reason == "" {
				t.Fatal("a skipped service must carry a reason")
			}
		})
	}
}

// TestProbeReachableUnreachable exercises the live probe against an httptest
// server (reachable) and a closed port (unreachable) — no real network.
func TestProbeReachableUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	defer srv.Close()

	if got := probe(&manifest.Service{BaseURL: srv.URL}); !strings.Contains(got, "reachable (HTTP 204)") {
		t.Fatalf("reachable probe = %q, want 'reachable (HTTP 204)'", got)
	}
	if got := probe(&manifest.Service{BaseURL: "http://127.0.0.1:1"}); !strings.HasPrefix(got, "unreachable:") {
		t.Fatalf("unreachable probe = %q, want 'unreachable: ...'", got)
	}
}
