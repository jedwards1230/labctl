package agentsafety

import (
	"strings"
	"testing"

	"github.com/jedwards1230/labctl/internal/manifest"
)

// TestMergeAuthPreview covers the redacted auth-line synthesis per strategy,
// including the noAuth short-circuit.
func TestMergeAuthPreview(t *testing.T) {
	tests := []struct {
		name    string
		auth    manifest.Auth
		noAuth  bool
		wantKey string
		wantVal string
	}{
		{"header-key", manifest.Auth{Strategy: "header-key", Header: "X-Api-Key"}, false, "X-Api-Key", "<redacted>"},
		{"bearer-default", manifest.Auth{Strategy: "bearer"}, false, "Authorization", "Bearer <redacted>"},
		{"bearer-scheme", manifest.Auth{Strategy: "bearer", Scheme: "Token"}, false, "Authorization", "Token <redacted>"},
		{"basic", manifest.Auth{Strategy: "basic"}, false, "Authorization", "Basic <redacted>"},
		{"oauth2", manifest.Auth{Strategy: "oauth2-client-credentials"}, false, "Authorization", "Bearer <redacted>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := MergeAuthPreview(nil, tc.auth, tc.noAuth)
			if out[tc.wantKey] != tc.wantVal {
				t.Errorf("%s = %q, want %q", tc.wantKey, out[tc.wantKey], tc.wantVal)
			}
		})
	}

	// noAuth omits the auth line entirely.
	if out := MergeAuthPreview(map[string]string{"X": "1"}, manifest.Auth{Strategy: "bearer"}, true); len(out) != 1 {
		t.Errorf("noAuth preview = %v, want only the passthrough header", out)
	}
}

// TestRedactSecretTokens replaces {secret.X} references and leaves other braces.
func TestRedactSecretTokens(t *testing.T) {
	if got := RedactSecretTokens(`{"k":"{secret.api_key}"}`); got != `{"k":"<redacted>"}` {
		t.Errorf("RedactSecretTokens = %q", got)
	}
}

// TestDryRun renders the request line, redacts a secret-bearing header value and
// body token, and uppercases the method.
func TestDryRun(t *testing.T) {
	out := DryRun("post", "https://h/api", map[string]string{"X-Token": "{secret.t}"}, []byte(`{"t":"{secret.t}"}`))
	if !strings.HasPrefix(out, "POST https://h/api\n") {
		t.Errorf("missing request line: %q", out)
	}
	if !strings.Contains(out, "X-Token: <redacted>") {
		t.Errorf("header not redacted: %q", out)
	}
	if !strings.Contains(out, `{"t":"<redacted>"}`) {
		t.Errorf("body token not redacted: %q", out)
	}
	if strings.Contains(out, "{secret.") {
		t.Errorf("leaked a secret template: %q", out)
	}
}
