package mcpserver

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net"
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

// isLoopbackAddr reports whether an http listen address (as passed to --http,
// e.g. ":9000", "127.0.0.1:9000", "[::1]:9000", "localhost:9000") binds only a
// loopback interface. A bare port (":9000") binds every interface (the
// net/http zero-value behavior) and is therefore treated as non-loopback —
// that is the case RequireAuth exists to catch. No DNS lookups are performed;
// only literal loopback IPs and the "localhost" literal are recognized as
// loopback, so an arbitrary hostname is conservatively treated as
// non-loopback (fail closed).
//
// A malformed address (net.SplitHostPort fails — e.g. a bare host with no
// port, such as an operator typo of "127.0.0.1" instead of "127.0.0.1:9000")
// is ALSO treated as non-loopback, never as loopback. Naively re-parsing the
// whole string as a host would misclassify exactly that typo as loopback
// (net.ParseIP("127.0.0.1") succeeds and IsLoopback() is true) and silently
// skip the auth requirement — the opposite of fail-closed. An address this
// ambiguous can't be trusted to be loopback, so it isn't; the resulting
// RequireAuth error is what surfaces the typo to the operator.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		return false // bare ":9000" binds every interface, not just loopback
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// RequireAuth enforces labctl's secure-by-default policy for the
// streamable-HTTP transport: a non-loopback --http bind must have an auth
// token configured (via LABCTL_MCP_AUTH_TOKEN or --auth-token-file) unless the
// operator explicitly opts out with allowUnauthenticated. Loopback binds
// (127.0.0.1, ::1, localhost) are unaffected — matches the existing implicit
// local-trust model, no auth requirement there.
//
// Returns nil when the server is cleared to start; a non-nil error carries an
// actionable message and is meant to be surfaced as a usage error (exit 2) by
// the caller, before any listener is ever opened.
func RequireAuth(addr, authToken string, allowUnauthenticated bool) error {
	if authToken != "" || allowUnauthenticated || isLoopbackAddr(addr) {
		return nil
	}
	return fmt.Errorf(
		"labctl mcp --http %s binds a non-loopback address (e.g., bare :PORT binds all interfaces) with no auth configured; "+
			"set %s or pass --auth-token-file <path> to require bearer auth on /mcp, "+
			"or pass --allow-unauthenticated to explicitly accept an unauthenticated non-loopback server (not recommended outside a trusted network)",
		addr, AuthTokenEnv,
	)
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
