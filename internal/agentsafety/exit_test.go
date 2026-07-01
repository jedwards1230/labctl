package agentsafety

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/secret"
	"github.com/jedwards1230/labctl/internal/transport"
)

// TestClassifyExitCodes pins the documented exit-code contract (plan §11):
// 0 ok, 2 usage, 3 auth, 4 HTTP≥400, 5 network, 6 decode, 1 general.
func TestClassifyExitCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"usage", NewUsageError("bad flag"), ExitUsage},
		{"auth", &transport.AuthError{Err: errors.New("401")}, ExitAuth},
		{"http", &transport.HTTPError{Status: 404, Method: "GET", URL: "u"}, ExitHTTP},
		{"rpc", &transport.RPCError{Code: -32000, Message: "boom"}, ExitHTTP},
		{"network", &transport.NetworkError{Err: errors.New("dial")}, ExitNetwork},
		{"decode", NewDecodeError(errors.New("jq")), ExitDecode},
		{"secret-auth", &secret.AuthError{Err: errors.New("op")}, ExitAuth},
		{"secret-config", &secret.ConfigError{Err: errors.New("bad source")}, ExitUsage},
		{"manifest-config", &manifest.ConfigError{Err: errors.New("bad config")}, ExitUsage},
		{"manifest-decode", &manifest.DecodeError{Err: errors.New("bad body")}, ExitDecode},
		{"general", errors.New("other"), ExitGeneral},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.err); got != tt.want {
				t.Errorf("Classify(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

// TestClassifyWrappedError confirms Classify uses errors.As, so a typed error
// wrapped with %w still maps to its code.
func TestClassifyWrappedError(t *testing.T) {
	wrapped := fmt.Errorf("context: %w", &transport.HTTPError{Status: 500})
	if got := Classify(wrapped); got != ExitHTTP {
		t.Errorf("wrapped HTTPError Classify = %d, want %d", got, ExitHTTP)
	}
}

// TestClassName maps each exit code to its symbolic name (and the default).
func TestClassName(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{ExitOK, "ok"},
		{ExitUsage, "usage"},
		{ExitAuth, "auth"},
		{ExitHTTP, "http"},
		{ExitNetwork, "network"},
		{ExitDecode, "decode"},
		{ExitGeneral, "error"},
		{999, "error"},
	}
	for _, tt := range tests {
		if got := ClassName(tt.code); got != tt.want {
			t.Errorf("ClassName(%d) = %q, want %q", tt.code, got, tt.want)
		}
	}
}

// TestHTTPStatus extracts the status from an *HTTPError anywhere in the chain,
// and reports ok=false for a non-HTTP error.
func TestHTTPStatus(t *testing.T) {
	wrapped := fmt.Errorf("ctx: %w", &transport.HTTPError{Status: 503})
	if status, ok := HTTPStatus(wrapped); !ok || status != 503 {
		t.Errorf("HTTPStatus(wrapped 503) = (%d, %v), want (503, true)", status, ok)
	}
	if status, ok := HTTPStatus(errors.New("plain")); ok || status != 0 {
		t.Errorf("HTTPStatus(plain) = (%d, %v), want (0, false)", status, ok)
	}
}
