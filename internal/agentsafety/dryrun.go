package agentsafety

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/transport"
)

// MergeAuthPreview adds a redacted line for the credential the auth strategy
// would set, so --dry-run shows it WITHOUT resolving the secret (no op call).
func MergeAuthPreview(headers map[string]string, a manifest.Auth, noAuth bool) map[string]string {
	out := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		out[k] = v
	}
	if noAuth {
		return out
	}
	switch a.Strategy {
	case "header-key":
		out[a.Header] = "<redacted>"
	case "bearer":
		scheme := a.Scheme
		if scheme == "" {
			scheme = "Bearer"
		}
		out["Authorization"] = scheme + " <redacted>"
	case "basic":
		out["Authorization"] = "Basic <redacted>"
	case "oauth2-client-credentials":
		out["Authorization"] = "Bearer <redacted>"
	}
	return out
}

// secretToken matches a {secret.X} template reference. Dry-run shows pre-expansion
// templates (it resolves no secrets), so a {secret.X} carries no credential value
// — but we still redact it in the preview so a secret-bearing custom header/body
// reads as <redacted>, consistently with the auth header, and never invites
// confusion about what a real request would carry.
var secretToken = regexp.MustCompile(`\{secret\.[^}]*\}`)

// RedactSecretTokens replaces every {secret.X} template reference with
// <redacted> so a dry-run preview never displays a secret-bearing template.
func RedactSecretTokens(s string) string {
	return secretToken.ReplaceAllString(s, "<redacted>")
}

// DryRun renders the byte-identical HTTP dry-run preview: a request line, one
// header line per header (auth values and {secret.X} tokens redacted), and the
// (redacted) body when present.
func DryRun(method, url string, headers map[string]string, body []byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", strings.ToUpper(method), url)
	for k, v := range headers {
		fmt.Fprintf(&b, "%s: %s\n", k, transport.RedactHeader(k, RedactSecretTokens(v)))
	}
	if len(body) > 0 {
		fmt.Fprintf(&b, "\n%s\n", RedactSecretTokens(string(body)))
	}
	return b.String()
}
