package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/template"
)

// tokenCacheEntry is the on-disk format for a cached access token.
type tokenCacheEntry struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// cacheDir returns the labctl cache directory, honoring XDG_CACHE_HOME.
func cacheDir() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "labctl")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".cache", "labctl")
	}
	return filepath.Join(home, ".cache", "labctl")
}

// cacheFileName returns the cache file path for the given client ID.
// Keyed by the full 64 hex chars of SHA-256(clientID) so the file name
// does not expose the client ID itself and has negligible collision probability.
func cacheFileName(dir, clientID string) string {
	sum := sha256.Sum256([]byte(clientID))
	key := fmt.Sprintf("%x", sum[:]) // full 32 bytes = 64 hex chars
	return filepath.Join(dir, key+".token")
}

// readCache loads a cached token from disk and returns it if still valid
// (more than 60 seconds before expiry). Returns empty string if absent,
// unreadable, malformed, expired, or written with insecure permissions.
func readCache(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if perm := info.Mode().Perm(); perm != 0o600 && perm != 0o400 {
		return "" // insecure permissions — discard
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var entry tokenCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return ""
	}
	if time.Now().Add(60 * time.Second).Before(entry.ExpiresAt) {
		return entry.AccessToken
	}
	return ""
}

// writeCache persists the token to disk with mode 0600.
func writeCache(path, token string, expiresIn int) error {
	entry := tokenCacheEntry{
		AccessToken: token,
		ExpiresAt:   time.Now().Add(time.Duration(expiresIn) * time.Second),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal token cache: %w", err)
	}
	// Write to a temp file and rename for atomicity.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write token cache: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup
		return fmt.Errorf("rename token cache: %w", err)
	}
	return nil
}

// fetchOAuth2Token resolves or refreshes the access token for the given
// auth spec. It uses the template env to expand client_id (Username field)
// and client_secret (Password field); the token URL is the Value field.
// An optional scope may be provided in Params[0].
//
// Cache location: <dir>/<sha256(clientID)>.token (0600), where the filename
// is the full 64 hex chars of SHA-256(clientID).
// Tokens are reused while valid with a 60-second safety margin.
func fetchOAuth2Token(ctx context.Context, a manifest.Auth, env template.Env, dir string) (string, error) {
	tokenURL, err := env.Expand(a.Value)
	if err != nil {
		return "", fmt.Errorf("oauth2: expand token URL: %w", err)
	}
	if tokenURL == "" {
		return "", fmt.Errorf("oauth2: auth.value (token URL) is required")
	}

	clientID, err := env.Expand(a.Username)
	if err != nil {
		return "", fmt.Errorf("oauth2: expand client_id: %w", err)
	}
	if clientID == "" {
		return "", fmt.Errorf("oauth2: auth.username (client_id) is required")
	}

	clientSecret, err := env.Expand(a.Password)
	if err != nil {
		return "", fmt.Errorf("oauth2: expand client_secret: %w", err)
	}
	if clientSecret == "" {
		return "", fmt.Errorf("oauth2: auth.password (client_secret) is required")
	}

	// Scope is optional; taken from Params[0] if present.
	var scope string
	if len(a.Params) > 0 {
		scope = strings.Join(a.Params, " ")
	}

	// Check disk cache before making a network call.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("oauth2: create cache dir: %w", err)
	}
	// TOCTOU guard: verify the cache dir is not a symlink and is owned by us.
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("oauth2: stat cache dir: %w", err)
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("oauth2: cache dir is a symlink (security risk)")
	}
	if stat, ok := dirInfo.Sys().(*syscall.Stat_t); ok && stat.Uid != uint32(os.Getuid()) {
		return "", fmt.Errorf("oauth2: cache dir not owned by current user")
	}
	cachePath := cacheFileName(dir, clientID)
	if tok := readCache(cachePath); tok != "" {
		return tok, nil
	}

	// Build the token request body.
	form := url.Values{"grant_type": {"client_credentials"}}
	if scope != "" {
		form.Set("scope", scope)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("oauth2: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(clientID, clientSecret)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("oauth2: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("oauth2: read token response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// Extract a human-readable error from the OAuth2 error response without
		// echoing back the client credentials.
		detail := extractOAuthError(body)
		return "", fmt.Errorf("oauth2: token endpoint %d: %s", resp.StatusCode, detail)
	}
	if resp.StatusCode != http.StatusOK {
		detail := extractOAuthError(body)
		return "", fmt.Errorf("oauth2: token endpoint %d: %s", resp.StatusCode, detail)
	}

	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("oauth2: decode token response: %w", err)
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("oauth2: token endpoint returned empty access_token")
	}
	if result.ExpiresIn <= 0 {
		result.ExpiresIn = 3600 // default 1h if missing
	}

	if err := writeCache(cachePath, result.AccessToken, result.ExpiresIn); err != nil {
		// Cache write failure is non-fatal — proceed with the token in memory.
		_, _ = fmt.Fprintf(os.Stderr, "labctl: oauth2 cache write: %v\n", err)
	}

	return result.AccessToken, nil
}

// extractOAuthError extracts a human-readable message from an OAuth2 error
// response body. Never returns the raw body (which may echo the credentials).
func extractOAuthError(body []byte) string {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return "check the OAuth client credentials"
	}
	for _, key := range []string{"error_description", "error", "message"} {
		if v, ok := obj[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return "check the OAuth client credentials"
}
