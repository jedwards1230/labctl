package cli

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedwards1230/labctl/internal/agentsafety"
	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/transport"
)

const openapiPetstoreFixture = `openapi: "3.0.3"
info:
  title: Pet Store
  version: "1.0"
paths:
  /pets:
    get:
      operationId: listPets
      summary: List all pets
      responses:
        "200": { description: ok }
components:
  securitySchemes:
    ApiKeyAuth:
      type: apiKey
      in: header
      name: X-Api-Key
security:
  - ApiKeyAuth: []
`

// TestCatalogAddOpenAPILocalFile drives the full `catalog add --openapi <file>
// --name X` CLI path against a local fixture (no network) and asserts the
// installed service resolves through the real loader, with the right auth.
func TestCatalogAddOpenAPILocalFile(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)

	specPath := filepath.Join(t.TempDir(), "petstore.yaml")
	if err := os.WriteFile(specPath, []byte(openapiPetstoreFixture), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "add", specPath, "--openapi", "--name", "petstore"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("add exit = %d, want 0 (stderr: %s)", code, errb.String())
	}

	manifestPath := filepath.Join(cfg, "catalogs", "petstore", "petstore.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("catalog manifest not installed: %v", err)
	}
	if strings.Contains(string(data), "base_url:") {
		t.Errorf("installed manifest should be portable (no base_url):\n%s", data)
	}

	// list shows the service with its catalog provenance.
	out.Reset()
	errb.Reset()
	if code := Run([]string{"list"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("list exit = %d (stderr: %s)", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("petstore")) {
		t.Errorf("list output should show petstore:\n%s", out.String())
	}

	// `catalog installed` reports type "openapi" with the source as the spec path.
	out.Reset()
	errb.Reset()
	if code := Run([]string{"catalog", "installed"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("installed exit = %d (stderr: %s)", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("openapi")) || !bytes.Contains(out.Bytes(), []byte(specPath)) {
		t.Errorf("`catalog installed` should list petstore (openapi, %s):\n%s", specPath, out.String())
	}

	// Bind a base_url and dry-run a command to prove the generated manifest
	// resolves and executes through the engine.
	bindBaseURL(t, cfg, "petstore", "http://example.test")
	out.Reset()
	errb.Reset()
	if code := Run([]string{"svc", "petstore", "listpets", "--dry-run"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("svc petstore listpets --dry-run exit = %d, want %d (stderr: %s)", code, agentsafety.ExitOK, errb.String())
	}
}

// TestCatalogAddOpenAPIInfersName confirms --name is optional when the
// document declares info.title.
func TestCatalogAddOpenAPIInfersName(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)

	specPath := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(specPath, []byte(openapiPetstoreFixture), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "add", specPath, "--openapi"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("add exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(cfg, "catalogs", "pet-store", "pet-store.yaml")); err != nil {
		t.Fatalf("expected catalog inferred as 'pet-store' from info.title: %v", err)
	}
}

// TestCatalogAddOpenAPINoTitleRequiresName confirms a spec lacking info.title
// requires --name with a clear error, and installs nothing.
func TestCatalogAddOpenAPINoTitleRequiresName(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)

	const noTitle = `openapi: "3.0.3"
info: { version: "1.0" }
paths: {}
`
	specPath := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(specPath, []byte(noTitle), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "add", specPath, "--openapi"}, &out, &errb); code != agentsafety.ExitUsage {
		t.Fatalf("exit = %d, want %d (usage) (stderr: %s)", code, agentsafety.ExitUsage, errb.String())
	}
	if !bytes.Contains(errb.Bytes(), []byte("--name")) {
		t.Errorf("stderr = %q, want guidance to pass --name", errb.String())
	}
	if _, err := os.ReadDir(filepath.Join(cfg, "catalogs")); err == nil {
		t.Error("nothing should be installed")
	}
}

// TestCatalogAddOpenAPIRefIncompatible confirms --ref cannot be combined with
// --openapi (ref is git-only).
func TestCatalogAddOpenAPIRefIncompatible(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)

	specPath := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(specPath, []byte(openapiPetstoreFixture), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "add", specPath, "--openapi", "--ref", "main"}, &out, &errb); code != agentsafety.ExitUsage {
		t.Fatalf("exit = %d, want %d (usage) (stderr: %s)", code, agentsafety.ExitUsage, errb.String())
	}
}

// TestCatalogAddOpenAPIFromHTTPServer drives the URL fetch path end-to-end
// against an httptest server.
func TestCatalogAddOpenAPIFromHTTPServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(openapiPetstoreFixture))
	}))
	defer srv.Close()

	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "add", srv.URL + "/openapi.yaml", "--openapi", "--name", "petstore"}, &out, &errb); code != agentsafety.ExitOK {
		t.Fatalf("add exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(cfg, "catalogs", "petstore", "petstore.yaml")); err != nil {
		t.Fatalf("catalog manifest not installed: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// fetchOpenAPISource hardening

// TestFetchOpenAPISourceRejectsNonHTTPScheme confirms only http(s):// URLs or
// local file paths are accepted — any other scheme is a clean usage error, not
// a misclassified local-file read attempt.
func TestFetchOpenAPISourceRejectsNonHTTPScheme(t *testing.T) {
	for _, src := range []string{"ftp://example.com/spec.yaml", "file:///etc/passwd", "ws://example.com/spec"} {
		t.Run(src, func(t *testing.T) {
			if _, err := fetchOpenAPISource(src); err == nil {
				t.Fatalf("fetchOpenAPISource(%q) should reject non-http(s) scheme", src)
			} else if _, ok := err.(*agentsafety.UsageError); !ok {
				t.Fatalf("fetchOpenAPISource(%q) error = %T, want *agentsafety.UsageError: %v", src, err, err)
			}
		})
	}
}

// TestFetchOpenAPISourceLocalFile confirms a plain path (no scheme) is read
// directly from disk.
func TestFetchOpenAPISourceLocalFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.yaml")
	if err := os.WriteFile(path, []byte(openapiPetstoreFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := fetchOpenAPISource(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != openapiPetstoreFixture {
		t.Errorf("fetchOpenAPISource read mismatch")
	}
}

// TestFetchOpenAPIURLSizeCap confirms a response exceeding the size cap fails
// rather than being read fully into memory, classified as a *manifest.DecodeError
// (exit 6) — an oversized/malformed document, not a transport failure.
func TestFetchOpenAPIURLSizeCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte("a"), 1<<20) // 1 MiB per write
		for i := 0; i < (openapiMaxBodyBytes/(1<<20))+2; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	_, err := fetchOpenAPIURL(srv.URL)
	if err == nil {
		t.Fatal("expected an error when the response exceeds the size cap")
	}
	var decErr *manifest.DecodeError
	if !errors.As(err, &decErr) {
		t.Fatalf("size-cap error = %T, want *manifest.DecodeError (exit 6): %v", err, err)
	}
	if code := agentsafety.Classify(err); code != agentsafety.ExitDecode {
		t.Errorf("agentsafety.Classify(size-cap error) = %d, want %d (decode)", code, agentsafety.ExitDecode)
	}
}

// TestFetchOpenAPIURLNon200 confirms a non-200 response is a clean error,
// classified as a *manifest.DecodeError (exit 6) — the endpoint answered but
// did not return the document.
func TestFetchOpenAPIURLNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetchOpenAPIURL(srv.URL)
	if err == nil {
		t.Fatal("expected an error for a 404 response")
	}
	var decErr *manifest.DecodeError
	if !errors.As(err, &decErr) {
		t.Fatalf("non-200 error = %T, want *manifest.DecodeError (exit 6): %v", err, err)
	}
	if code := agentsafety.Classify(err); code != agentsafety.ExitDecode {
		t.Errorf("agentsafety.Classify(non-200 error) = %d, want %d (decode)", code, agentsafety.ExitDecode)
	}
}

// TestFetchOpenAPIURLNetworkError confirms a genuine transport failure
// (connection refused) is classified as a *transport.NetworkError (exit 5),
// not a decode/usage error — dialing a closed listener never gets an HTTP
// response to classify on status.
func TestFetchOpenAPIURLNetworkError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	// Nothing is listening on addr anymore — Do() should fail with connection
	// refused.

	_, err = fetchOpenAPIURL("http://" + addr + "/openapi.yaml")
	if err == nil {
		t.Fatal("expected an error dialing a closed listener")
	}
	var netErr *transport.NetworkError
	if !errors.As(err, &netErr) {
		t.Fatalf("connection-refused error = %T, want *transport.NetworkError (exit 5): %v", err, err)
	}
	if code := agentsafety.Classify(err); code != agentsafety.ExitNetwork {
		t.Errorf("agentsafety.Classify(connection-refused error) = %d, want %d (network)", code, agentsafety.ExitNetwork)
	}
}

// TestCatalogAddOpenAPINetworkErrorExitCode drives the full `catalog add
// --openapi` CLI path against an unreachable URL and asserts the process exit
// code is 5 (network), not the generic agentsafety.ExitGeneral fallback.
func TestCatalogAddOpenAPINetworkErrorExitCode(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	code := Run([]string{"catalog", "add", "http://" + addr + "/openapi.yaml", "--openapi", "--name", "unreachable"}, &out, &errb)
	if code != agentsafety.ExitNetwork {
		t.Fatalf("exit = %d, want %d (network) (stderr: %s)", code, agentsafety.ExitNetwork, errb.String())
	}
}

// TestFetchOpenAPIURLRedirectCap confirms an infinite redirect chain is
// bounded rather than followed forever.
func TestFetchOpenAPIURLRedirectCap(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
	}))
	defer srv.Close()

	if _, err := fetchOpenAPIURL(srv.URL); err == nil {
		t.Fatal("expected an error for an unbounded redirect chain")
	}
}

// TestFetchOpenAPIURLRejectsNonHTTPRedirect confirms a redirect to a
// non-http(s) scheme (e.g. file:// — an attempted local-file read via
// redirect) is rejected by the CheckRedirect scheme guard rather than
// followed, so a malicious/compromised OpenAPI endpoint can't use a 3xx to
// make labctl read an arbitrary local file.
func TestFetchOpenAPIURLRejectsNonHTTPRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "file:///etc/passwd")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	b, err := fetchOpenAPIURL(srv.URL)
	if err == nil {
		t.Fatalf("expected the file:// redirect to be rejected, got body: %s", b)
	}
	if strings.Contains(err.Error(), "root:") {
		t.Fatalf("error suggests /etc/passwd was actually read: %v", err)
	}
}
