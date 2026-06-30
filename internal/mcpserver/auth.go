package mcpserver

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// AuthTokenEnv is the environment variable that holds the bearer token used to
// guard the streamable-HTTP /mcp endpoint. When unset or empty, HTTP auth is
// disabled (default-off, backward-compatible). The token is never logged.
const AuthTokenEnv = "LABCTL_MCP_AUTH_TOKEN"

// ResolveAuthToken returns the bearer token that should guard the /mcp
// endpoint, or "" when auth is disabled.
//
// Precedence: --auth-token-file (when non-empty) wins over the
// LABCTL_MCP_AUTH_TOKEN env var. If tokenFile is set but the file is
// unreadable or empty the call fails with an error — an operator who asked
// for file-based auth must not silently fall through to no-auth (fail-closed).
// If tokenFile is empty, the trimmed value of LABCTL_MCP_AUTH_TOKEN is
// returned; empty/unset means auth is disabled.
func ResolveAuthToken(tokenFile string) (string, error) {
	if tokenFile != "" {
		data, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("reading auth token file %q: %w", tokenFile, err)
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", fmt.Errorf("auth token file %q is empty", tokenFile)
		}
		return token, nil
	}
	return strings.TrimSpace(os.Getenv(AuthTokenEnv)), nil
}

// bearerAuthMiddleware returns an http.Handler that requires the request to
// carry a valid "Authorization: Bearer <token>" header. On a missing or
// invalid token it writes 401 Unauthorized with a WWW-Authenticate header and
// a short body; on a valid token it calls next.
//
// The comparison uses crypto/subtle.ConstantTimeCompare against the full
// expected "Bearer <token>" string so the per-byte value comparison does not
// short-circuit. Note ConstantTimeCompare is only constant-time for equal-length
// inputs — it returns 0 immediately when lengths differ, leaking the token
// length, which is not sensitive here (a random token's length carries no
// secret). The token value itself is never written to logs or response bodies.
func bearerAuthMiddleware(token string, next http.Handler) http.Handler {
	// Pre-build the expected full header value once.
	expected := "Bearer " + token
	expectedBytes := []byte(expected)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(got), expectedBytes) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="labctl", error="invalid_token"`)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, "unauthorized\n")
			return
		}
		next.ServeHTTP(w, r)
	})
}
