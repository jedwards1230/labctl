package mcpserver

import (
	"net/http"
	"testing"
	"time"
)

// TestNewHTTPServerTimeouts pins the HTTP server timeout policy. WriteTimeout
// must stay 0 (unlimited) so long-lived streamable-HTTP MCP SSE responses are
// not truncated mid-stream; the read/idle bounds add resource-exhaustion
// protection without affecting the streaming contract.
func TestNewHTTPServerTimeouts(t *testing.T) {
	srv := newHTTPServer(":9000", http.NewServeMux())

	if got, want := srv.Addr, ":9000"; got != want {
		t.Errorf("Addr = %q, want %q", got, want)
	}
	if got, want := srv.ReadHeaderTimeout, 10*time.Second; got != want {
		t.Errorf("ReadHeaderTimeout = %v, want %v", got, want)
	}
	if got, want := srv.ReadTimeout, 60*time.Second; got != want {
		t.Errorf("ReadTimeout = %v, want %v", got, want)
	}
	if got, want := srv.IdleTimeout, 120*time.Second; got != want {
		t.Errorf("IdleTimeout = %v, want %v", got, want)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0 (unlimited) so streamed MCP responses are not truncated", srv.WriteTimeout)
	}
}
