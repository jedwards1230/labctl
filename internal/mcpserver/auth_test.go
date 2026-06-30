package mcpserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/jedwards1230/labctl/internal/mcpserver"
)

// ── ResolveAuthToken ──────────────────────────────────────────────────────────

// TestResolveAuthToken_FileWins verifies that a non-empty token file is read,
// whitespace-trimmed, and returned when tokenFile is provided.
func TestResolveAuthToken_FileWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("  secret42\n"), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := mcpserver.ResolveAuthToken(path)
	if err != nil {
		t.Fatalf("ResolveAuthToken: %v", err)
	}
	if got != "secret42" {
		t.Errorf("token = %q, want \"secret42\"", got)
	}
}

// TestResolveAuthToken_EmptyFile verifies that an empty (or whitespace-only)
// token file returns an error (fail-closed: the operator opted into file-based
// auth but the file is empty — silently falling back to no-auth is wrong).
func TestResolveAuthToken_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty")
	if err := os.WriteFile(path, []byte("  \n"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := mcpserver.ResolveAuthToken(path)
	if err == nil {
		t.Fatal("expected an error for an empty token file, got nil")
	}
}

// TestResolveAuthToken_MissingFile verifies that a missing token file returns
// an error rather than silently disabling auth.
func TestResolveAuthToken_MissingFile(t *testing.T) {
	_, err := mcpserver.ResolveAuthToken("/no/such/file/token.txt")
	if err == nil {
		t.Fatal("expected an error for a missing token file, got nil")
	}
}

// TestResolveAuthToken_EnvFallback verifies that when tokenFile is empty the
// env var LABCTL_MCP_AUTH_TOKEN is used (trimmed).
func TestResolveAuthToken_EnvFallback(t *testing.T) {
	t.Setenv(mcpserver.AuthTokenEnv, "  envtoken  ")

	got, err := mcpserver.ResolveAuthToken("")
	if err != nil {
		t.Fatalf("ResolveAuthToken: %v", err)
	}
	if got != "envtoken" {
		t.Errorf("token = %q, want \"envtoken\"", got)
	}
}

// TestResolveAuthToken_EnvEmpty verifies that an empty/unset env var returns
// "" without error (auth disabled).
func TestResolveAuthToken_EnvEmpty(t *testing.T) {
	t.Setenv(mcpserver.AuthTokenEnv, "")

	got, err := mcpserver.ResolveAuthToken("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("token = %q, want empty (auth disabled)", got)
	}
}

// TestResolveAuthToken_FileWinsOverEnv verifies the documented precedence:
// --auth-token-file (tokenFile non-empty) wins over the env var.
func TestResolveAuthToken_FileWinsOverEnv(t *testing.T) {
	t.Setenv(mcpserver.AuthTokenEnv, "env-secret")

	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("file-secret"), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := mcpserver.ResolveAuthToken(path)
	if err != nil {
		t.Fatalf("ResolveAuthToken: %v", err)
	}
	if got != "file-secret" {
		t.Errorf("token = %q, want \"file-secret\" (file must win over env)", got)
	}
}

// ── bearer-token middleware via NewHTTPHandler ────────────────────────────────

// TestHTTPHandlerBearerAuth exercises the auth middleware through a real
// httptest server using direct HTTP (not the MCP SDK), so each case can
// inspect the exact status code.
//
// Coverage:
//   - correct token → middleware passes through (status ≠ 401); the MCP
//     handler itself returns 400 for a bare GET without an MCP body — that
//     is expected and correct; the test asserts "not rejected by auth"
//   - wrong token   → 401 with WWW-Authenticate header
//   - missing token → 401
//   - GET /healthz  → 200 even without any Authorization header (probe stays open)
func TestHTTPHandlerBearerAuth(t *testing.T) {
	const token = "super-secret-token"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	loaded := buildTestLoaded(upstream.URL)
	tracer := noop.NewTracerProvider().Tracer("test")
	handler := mcpserver.NewHTTPHandler(loaded, loaded.Config, "v9.9.9", tracer, nil, mcpserver.Options{}, token)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	cases := []struct {
		name        string
		path        string
		authHeader  string // "" means omit the header
		wantExact   int    // when > 0: assert exact status code
		wantNot401  bool   // when true: assert status ≠ 401 (middleware passed through)
		wantWWWAuth bool   // expect WWW-Authenticate header on 401
	}{
		{
			name:       "healthz no auth required",
			path:       "/healthz",
			authHeader: "", // no header — healthz must stay open
			wantExact:  http.StatusOK,
		},
		{
			// A bare GET to /mcp without a proper MCP body will receive 400
			// from the MCP handler itself — but not 401 from the middleware.
			// The assertion here is: auth passed, the underlying handler ran.
			name:       "mcp correct token",
			path:       "/mcp",
			authHeader: "Bearer " + token,
			wantNot401: true,
		},
		{
			name:        "mcp wrong token",
			path:        "/mcp",
			authHeader:  "Bearer wrong-token",
			wantExact:   http.StatusUnauthorized,
			wantWWWAuth: true,
		},
		{
			name:        "mcp missing token",
			path:        "/mcp",
			authHeader:  "", // omitted
			wantExact:   http.StatusUnauthorized,
			wantWWWAuth: true,
		},
	}

	client := &http.Client{Timeout: 5 * time.Second}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, srv.URL+tc.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if tc.wantExact > 0 && resp.StatusCode != tc.wantExact {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantExact)
			}
			if tc.wantNot401 && resp.StatusCode == http.StatusUnauthorized {
				t.Errorf("status = 401 (Unauthorized); expected middleware to pass through for correct token")
			}
			if tc.wantWWWAuth {
				if resp.Header.Get("WWW-Authenticate") == "" {
					t.Error("expected WWW-Authenticate header on 401 response, got none")
				}
			}
		})
	}
}

// TestHTTPHandlerNoAuthBackwardCompat verifies that when authToken is "" the
// /mcp endpoint is reachable without any Authorization header (no regression
// for the no-auth default). The existing TestHTTPHandlerRoundTrip already
// covers the full SDK round-trip; this test pins the backward-compat contract
// for direct HTTP callers.
func TestHTTPHandlerNoAuthBackwardCompat(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	loaded := buildTestLoaded(upstream.URL)
	tracer := noop.NewTracerProvider().Tracer("test")
	// authToken == "" → no middleware wrapped, endpoint open.
	handler := mcpserver.NewHTTPHandler(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{}, "")

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// A bare GET to /mcp without any Authorization header must not return 401.
	resp, err := http.Get(srv.URL + "/mcp") //nolint:noctx // test-only, no cancellation needed
	if err != nil {
		t.Fatalf("GET /mcp: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("status = 401 (Unauthorized) with no auth configured; want non-401")
	}
}

// authHeaderTransport is an http.RoundTripper that injects a fixed set of
// headers on every request. Used in tests to supply bearer credentials to the
// SDK's StreamableClientTransport (which accepts an *http.Client but has no
// dedicated header field).
type authHeaderTransport struct {
	base    http.RoundTripper
	headers http.Header
}

func (t *authHeaderTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, vals := range t.headers {
		// Wholesale assignment preserves multi-value headers (a per-value Set
		// loop would collapse them to the last value).
		r.Header[http.CanonicalHeaderKey(k)] = vals
	}
	return t.base.RoundTrip(r)
}

// TestHTTPHandlerBearerAuthRoundTrip verifies a full MCP SDK round-trip over
// streamable-HTTP with a bearer token configured. The SDK transport is given a
// custom http.Client that injects the Authorization header, and must complete
// initialize + tools/list successfully.
func TestHTTPHandlerBearerAuthRoundTrip(t *testing.T) {
	const token = "round-trip-token"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	loaded := buildTestLoaded(upstream.URL)
	tracer := noop.NewTracerProvider().Tracer("test")
	handler := mcpserver.NewHTTPHandler(loaded, loaded.Config, "v9.9.9", tracer, nil, mcpserver.Options{}, token)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Inject the bearer token via a custom RoundTripper on the HTTPClient field
	// — StreamableClientTransport has no dedicated header slot.
	authedClient := &http.Client{
		Transport: &authHeaderTransport{
			base:    http.DefaultTransport,
			headers: http.Header{"Authorization": {"Bearer " + token}},
		},
	}
	mcpTransport := &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: authedClient,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-authed-client", Version: "v0.0.1"}, nil)
	session, err := client.Connect(ctx, mcpTransport, nil)
	if err != nil {
		t.Fatalf("Connect (initialize) with correct token: %v", err)
	}
	defer func() { _ = session.Close() }()

	// tools/list should succeed.
	var found bool
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("Tools iteration: %v", err)
		}
		if tool.Name == "testsvc_ping" {
			found = true
		}
	}
	if !found {
		t.Fatal("testsvc_ping not found in tools/list over authed streamable-HTTP")
	}
}
