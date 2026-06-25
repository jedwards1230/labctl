package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/template"
)

// fakeTokenServer returns a test server whose handler responds to
// POST /token with the given status and JSON body. tokenCalls is
// incremented on each request.
func fakeTokenServer(t *testing.T, status int, body string, tokenCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			tokenCalls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
}

func authSpec(tokenURL string) manifest.Auth {
	return manifest.Auth{
		Strategy: "oauth2-client-credentials",
		Value:    tokenURL,
		Username: "test-client-id",
		Password: "test-client-secret",
	}
}

// env with no secret resolution needed (username/password are literals, not {secret.X}).
var plainEnv = template.Env{}

func TestFetchOAuth2TokenFetch(t *testing.T) {
	var calls atomic.Int32
	srv := fakeTokenServer(t, 200,
		`{"access_token":"test-tok","token_type":"Bearer","expires_in":3600}`,
		&calls,
	)
	defer srv.Close()

	dir := t.TempDir()
	a := authSpec(srv.URL)

	tok, err := fetchOAuth2Token(context.Background(), a, plainEnv, dir)
	if err != nil {
		t.Fatalf("fetchOAuth2Token: %v", err)
	}
	if tok != "test-tok" {
		t.Fatalf("token = %q, want test-tok", tok)
	}
	if calls.Load() != 1 {
		t.Fatalf("token endpoint called %d times, want 1", calls.Load())
	}
}

func TestFetchOAuth2TokenCacheHit(t *testing.T) {
	var calls atomic.Int32
	srv := fakeTokenServer(t, 200,
		`{"access_token":"test-tok","token_type":"Bearer","expires_in":3600}`,
		&calls,
	)
	defer srv.Close()

	dir := t.TempDir()
	a := authSpec(srv.URL)

	// First call — should POST to token endpoint.
	tok1, err := fetchOAuth2Token(context.Background(), a, plainEnv, dir)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}

	// Second call — should reuse the cache, no second POST.
	tok2, err := fetchOAuth2Token(context.Background(), a, plainEnv, dir)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}

	if tok1 != tok2 {
		t.Errorf("tokens differ: %q vs %q", tok1, tok2)
	}
	if calls.Load() != 1 {
		t.Fatalf("token endpoint called %d times after two fetches, want 1 (cache should hit)", calls.Load())
	}
}

func TestFetchOAuth2TokenCachePerms(t *testing.T) {
	var calls atomic.Int32
	srv := fakeTokenServer(t, 200,
		`{"access_token":"test-tok","token_type":"Bearer","expires_in":3600}`,
		&calls,
	)
	defer srv.Close()

	dir := t.TempDir()
	a := authSpec(srv.URL)

	if _, err := fetchOAuth2Token(context.Background(), a, plainEnv, dir); err != nil {
		t.Fatalf("fetchOAuth2Token: %v", err)
	}

	// Find the cache file and check its permissions.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var cacheFile string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".token" {
			cacheFile = filepath.Join(dir, e.Name())
			break
		}
	}
	if cacheFile == "" {
		t.Fatal("no .token cache file found")
	}
	info, err := os.Stat(cacheFile)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("cache file mode = %04o, want 0600", perm)
	}
}

func TestFetchOAuth2TokenExpired(t *testing.T) {
	var calls atomic.Int32
	srv := fakeTokenServer(t, 200,
		`{"access_token":"fresh-tok","token_type":"Bearer","expires_in":3600}`,
		&calls,
	)
	defer srv.Close()

	dir := t.TempDir()
	a := authSpec(srv.URL)

	// Write a stale cache entry (expired 1 second ago).
	expired := tokenCacheEntry{
		AccessToken: "stale-tok",
		ExpiresAt:   time.Now().Add(-time.Second),
	}
	data, _ := json.Marshal(expired)
	cachePath := cacheFileName(dir, "test-client-id")
	if err := os.WriteFile(cachePath, data, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tok, err := fetchOAuth2Token(context.Background(), a, plainEnv, dir)
	if err != nil {
		t.Fatalf("fetchOAuth2Token: %v", err)
	}
	if tok != "fresh-tok" {
		t.Errorf("token = %q, want fresh-tok (expired cache should be bypassed)", tok)
	}
	if calls.Load() != 1 {
		t.Fatalf("token endpoint called %d times, want 1 (expired cache → refetch)", calls.Load())
	}
}

func TestFetchOAuth2Token401(t *testing.T) {
	var calls atomic.Int32
	srv := fakeTokenServer(t, 401,
		`{"error":"invalid_client","error_description":"bad credentials"}`,
		&calls,
	)
	defer srv.Close()

	dir := t.TempDir()
	a := authSpec(srv.URL)

	_, err := fetchOAuth2Token(context.Background(), a, plainEnv, dir)
	if err == nil {
		t.Fatal("expected error from 401 token endpoint, got nil")
	}
}

func TestApplyOAuth2ClientCredentials(t *testing.T) {
	var calls atomic.Int32
	srv := fakeTokenServer(t, 200,
		`{"access_token":"apply-tok","token_type":"Bearer","expires_in":3600}`,
		&calls,
	)
	defer srv.Close()

	// Override cacheDir to use a temp dir for this test.
	// We do this by using a custom env that has the XDG override.
	dir := t.TempDir()

	t.Setenv("XDG_CACHE_HOME", dir)

	a := manifest.Auth{
		Strategy: "oauth2-client-credentials",
		Value:    srv.URL,
		Username: "test-client-id",
		Password: "test-client-secret",
	}

	req, err := http.NewRequest(http.MethodGet, "http://example.test/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	applier := New(a, plainEnv)
	if err := applier.Apply(req, false); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := req.Header.Get("Authorization")
	if got != "Bearer apply-tok" {
		t.Errorf("Authorization = %q, want 'Bearer apply-tok'", got)
	}
}
