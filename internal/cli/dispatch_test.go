package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// writeService writes a manifest into <dir>/services/<name>.yaml, creating the
// services dir if needed.
func writeService(t *testing.T, dir, name, body string) {
	t.Helper()
	svcDir := filepath.Join(dir, "services")
	if err := os.MkdirAll(svcDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svcDir, name+".yaml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

// TestDispatchSuccess drives a successful service command end-to-end through
// Run(): it stands up an httptest server, points a no-auth manifest at it via a
// temp config dir, and asserts the rendered body reaches stdout with exit 0.
// No `op` call and no live network — the auth strategy is "none".
func TestDispatchSuccess(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/api/v3/movie" {
			t.Errorf("path = %q, want /api/v3/movie", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":1,"title":"A"},{"id":2,"title":"B"}]`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeService(t, dir, "radarr", `
name: radarr
base_url: `+srv.URL+`
auth:
  strategy: none
commands:
  list:
    method: GET
    path: /api/v3/movie
    output:
      filter: map(.id)
`)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"svc", "radarr", "list"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("server hit %d times, want 1", hits.Load())
	}
	// Default filter `map(.id)` over the body → JSON array [1,2] (pretty-printed).
	got := out.String()
	if !strings.Contains(got, "1") || !strings.Contains(got, "2") {
		t.Fatalf("stdout = %q, want filtered ids 1 and 2", got)
	}
	// The titles must NOT survive the map(.id) filter.
	if strings.Contains(got, "title") {
		t.Fatalf("stdout = %q, filter map(.id) should have dropped titles", got)
	}
}

// TestDispatchSecretFailureExit3 proves a credential failure routed through a
// QUERY-param secret exits 3 (auth), matching the auth-strategy path. The secret
// resolver command points at a nonexistent binary so the failure is hermetic —
// no real `op`, no network (the resolver fails before any request is sent).
func TestDispatchSecretFailureExit3(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	// config.yaml: a secret resolver command that cannot run.
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`
secret:
  command: ["labctl-test-no-such-op-binary"]
`), 0600); err != nil {
		t.Fatal(err)
	}
	writeService(t, dir, "radarr", `
name: radarr
base_url: `+srv.URL+`
auth:
  strategy: none
secrets:
  api_key:
    ref: op://homelab/Radarr/api_key
commands:
  list:
    method: GET
    path: /api/v3/movie
    query: apikey={secret.api_key}
`)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"svc", "radarr", "list"}, &out, &errb); code != exitAuth {
		t.Fatalf("exit = %d, want %d (auth) (stderr: %s)", code, exitAuth, errb.String())
	}
	if hits.Load() != 0 {
		t.Fatalf("server hit %d times, want 0 (secret must fail before the request)", hits.Load())
	}
}

// TestDispatchRawOutput asserts that `-o raw` bypasses the command's default
// filter and prints the verbatim response body.
func TestDispatchRawOutput(t *testing.T) {
	const raw = `[{"id":1,"title":"A"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(raw))
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeService(t, dir, "radarr", `
name: radarr
base_url: `+srv.URL+`
auth:
  strategy: none
commands:
  list:
    method: GET
    path: /api/v3/movie
    output:
      filter: map(.id)
`)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"-o", "raw", "svc", "radarr", "list"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if strings.TrimSpace(out.String()) != raw {
		t.Fatalf("raw stdout = %q, want verbatim body %q", out.String(), raw)
	}
}

// TestDispatchFilterFlag asserts the --filter flag overrides the command's
// default filter.
func TestDispatchFilterFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":1,"title":"A"},{"id":2,"title":"B"}]`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeService(t, dir, "radarr", `
name: radarr
base_url: `+srv.URL+`
auth:
  strategy: none
commands:
  list:
    method: GET
    path: /api/v3/movie
    output:
      filter: map(.id)
`)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	// Override map(.id) with a filter that extracts titles instead.
	if code := Run([]string{"--filter", "map(.title)", "svc", "radarr", "list"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, `"A"`) || !strings.Contains(got, `"B"`) {
		t.Fatalf("stdout = %q, want titles A and B from override filter", got)
	}
	if strings.Contains(got, `"id"`) {
		t.Fatalf("stdout = %q, override filter map(.title) should not surface id", got)
	}
}

// TestDispatchHTTPErrorExit4 asserts a ≥400 response maps to exit 4.
func TestDispatchHTTPErrorExit4(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeService(t, dir, "radarr", `
name: radarr
base_url: `+srv.URL+`
auth:
  strategy: none
commands:
  list:
    method: GET
    path: /api/v3/movie
`)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"svc", "radarr", "list"}, &out, &errb); code != exitHTTP {
		t.Fatalf("exit = %d, want %d (HTTP) (stderr: %s)", code, exitHTTP, errb.String())
	}
}

// TestDispatchDecodeErrorExit6 asserts a body that cannot be decoded for the
// configured json filter maps to exit 6.
func TestDispatchDecodeErrorExit6(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`this is not json`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeService(t, dir, "radarr", `
name: radarr
base_url: `+srv.URL+`
auth:
  strategy: none
commands:
  list:
    method: GET
    path: /api/v3/movie
    output:
      filter: map(.id)
`)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"svc", "radarr", "list"}, &out, &errb); code != exitDecode {
		t.Fatalf("exit = %d, want %d (decode) (stderr: %s)", code, exitDecode, errb.String())
	}
}

// TestDispatchDryRunNoNetwork asserts --dry-run resolves and prints the request
// without sending it: the server must never be hit, stdout shows the request
// line, and exit is 0.
func TestDispatchDryRunNoNetwork(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeService(t, dir, "radarr", `
name: radarr
base_url: `+srv.URL+`
auth:
  strategy: none
commands:
  list:
    method: GET
    path: /api/v3/movie
`)
	t.Setenv("LABCTL_CONFIG_DIR", dir)

	var out, errb bytes.Buffer
	if code := Run([]string{"--dry-run", "svc", "radarr", "list"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if hits.Load() != 0 {
		t.Fatalf("dry-run hit the server %d times, want 0", hits.Load())
	}
	if !strings.Contains(out.String(), "GET "+srv.URL+"/api/v3/movie") {
		t.Fatalf("dry-run stdout = %q, want the resolved request line", out.String())
	}
}
