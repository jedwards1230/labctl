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
	cachePath := cacheFileName(dir, "test-client-id", a.Value, "")
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

func TestFetchOAuth2TokenCacheDirSymlink(t *testing.T) {
	var calls atomic.Int32
	srv := fakeTokenServer(t, 200,
		`{"access_token":"test-tok","token_type":"Bearer","expires_in":3600}`,
		&calls,
	)
	defer srv.Close()

	// Create a real directory and a symlink pointing at it.
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	if err := os.Mkdir(realDir, 0700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	symlinkDir := filepath.Join(base, "symlink")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	a := authSpec(srv.URL)
	_, err := fetchOAuth2Token(context.Background(), a, plainEnv, symlinkDir)
	if err == nil {
		t.Fatal("expected error when cache dir is a symlink, got nil")
	}
	if calls.Load() != 0 {
		t.Fatalf("token endpoint called %d times, want 0 (should have failed before network call)", calls.Load())
	}
}

// TestReadCacheInsecurePerms verifies that readCache ignores a cache file whose
// permissions are broader than 0600/0400 (e.g. world-readable 0644).
func TestReadCacheInsecurePerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "insecure.token")

	// Write a valid, non-expired cache entry — but with insecure 0644 permissions.
	entry := tokenCacheEntry{
		AccessToken: "should-be-ignored",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if tok := readCache(path); tok != "" {
		t.Errorf("readCache with 0644 file returned %q, want empty string (insecure perms must be rejected)", tok)
	}
}

// TestReadCacheEvictsExpired verifies that readCache deletes an expired cache
// file from disk (so a stale bearer token does not linger) while leaving a
// valid, non-expired cache file in place.
func TestReadCacheEvictsExpired(t *testing.T) {
	t.Run("expired entry is removed", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "expired.token")

		entry := tokenCacheEntry{
			AccessToken: "stale-tok",
			ExpiresAt:   time.Now().Add(-time.Second), // already expired
		}
		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		if tok := readCache(path); tok != "" {
			t.Errorf("readCache returned %q for expired entry, want empty", tok)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expired cache file still present (stat err = %v), want removed", err)
		}
	})

	t.Run("valid entry is retained", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "valid.token")

		entry := tokenCacheEntry{
			AccessToken: "fresh-tok",
			ExpiresAt:   time.Now().Add(time.Hour), // well outside the margin
		}
		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		if tok := readCache(path); tok != "fresh-tok" {
			t.Errorf("readCache returned %q, want fresh-tok", tok)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("valid cache file was removed or unreadable: %v", err)
		}
	})

	t.Run("symlink path is refused without touching the target", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "secret-target")
		if err := os.WriteFile(target, []byte("do-not-delete"), 0600); err != nil {
			t.Fatalf("WriteFile target: %v", err)
		}
		link := filepath.Join(dir, "link.token")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		// readCache must Lstat-reject the symlink: return empty AND never
		// remove/follow it (the target must survive untouched).
		if tok := readCache(link); tok != "" {
			t.Errorf("readCache followed a symlink, returned %q, want empty", tok)
		}
		if _, err := os.Lstat(link); err != nil {
			t.Errorf("symlink itself was removed: %v", err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Errorf("symlink target was removed/altered: %v", err)
		}
	})
}

// TestCacheFileNameKeying proves the cache filename depends on client ID, token
// URL, and scope — two endpoints sharing a client_id but differing in token URL
// or scope must not collide, while identical inputs map to the same file.
func TestCacheFileNameKeying(t *testing.T) {
	dir := "/cache"
	base := cacheFileName(dir, "client", "https://idp.a/token", "read")

	if got := cacheFileName(dir, "client", "https://idp.a/token", "read"); got != base {
		t.Errorf("identical inputs gave different filenames: %q vs %q", got, base)
	}
	if got := cacheFileName(dir, "client", "https://idp.b/token", "read"); got == base {
		t.Error("different token URL must yield a different cache filename")
	}
	if got := cacheFileName(dir, "client", "https://idp.a/token", "write"); got == base {
		t.Error("different scope must yield a different cache filename")
	}
	if got := cacheFileName(dir, "other", "https://idp.a/token", "read"); got == base {
		t.Error("different client ID must yield a different cache filename")
	}
}

// TestFetchOAuth2FieldAliases proves the intent-revealing token_url/client_id/
// client_secret fields and the legacy value/username/password fields resolve to
// the SAME token request — same basic-auth credentials, same token endpoint —
// so older manifests keep working unchanged (back-compat).
func TestFetchOAuth2FieldAliases(t *testing.T) {
	// A server that captures the basic-auth credentials it was sent.
	newServer := func(t *testing.T) (*httptest.Server, *string, *string) {
		t.Helper()
		var gotUser, gotPass string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUser, gotPass, _ = r.BasicAuth()
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"access_token":"alias-tok","token_type":"Bearer","expires_in":3600}`)
		}))
		return srv, &gotUser, &gotPass
	}

	cases := []struct {
		name string
		spec func(tokenURL string) manifest.Auth
	}{
		{
			name: "new fields (token_url/client_id/client_secret)",
			spec: func(tokenURL string) manifest.Auth {
				return manifest.Auth{
					Strategy:     "oauth2-client-credentials",
					TokenURL:     tokenURL,
					ClientID:     "cid-123",
					ClientSecret: "csecret-456",
				}
			},
		},
		{
			name: "legacy fields (value/username/password)",
			spec: func(tokenURL string) manifest.Auth {
				return manifest.Auth{
					Strategy: "oauth2-client-credentials",
					Value:    tokenURL,
					Username: "cid-123",
					Password: "csecret-456",
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, gotUser, gotPass := newServer(t)
			defer srv.Close()

			tok, err := fetchOAuth2Token(context.Background(), tc.spec(srv.URL), plainEnv, t.TempDir())
			if err != nil {
				t.Fatalf("fetchOAuth2Token: %v", err)
			}
			if tok != "alias-tok" {
				t.Fatalf("token = %q, want alias-tok", tok)
			}
			if *gotUser != "cid-123" || *gotPass != "csecret-456" {
				t.Fatalf("basic auth = %q:%q, want cid-123:csecret-456", *gotUser, *gotPass)
			}
		})
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
