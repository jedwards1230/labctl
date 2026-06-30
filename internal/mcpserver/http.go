package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace"

	"github.com/jedwards1230/labctl/internal/manifest"
)

// MCP endpoint and health paths served by the streamable-HTTP transport.
const (
	mcpPath    = "/mcp"
	healthPath = "/healthz"
)

// NewHTTPHandler builds the MCP server from the loaded manifests and returns an
// http.Handler exposing it as a streamable-HTTP MCP endpoint at /mcp plus a
// GET /healthz liveness probe. The tool set is process-wide: every session
// reuses the single prebuilt server. It reuses BuildServer, so the tool
// registration (and thus behaviour) is identical to the stdio transport.
func NewHTTPHandler(
	loaded *manifest.Loaded,
	cfg manifest.Config,
	version string,
	tracer trace.Tracer,
	stderr io.Writer,
	opts Options,
) http.Handler {
	srv := BuildServer(loaded, cfg, version, tracer, stderr, opts)

	// getServer returns the single prebuilt server for every session — the tool
	// set is process-wide, not per-request.
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		nil,
	)

	mux := http.NewServeMux()
	mux.Handle(mcpPath, handler)
	mux.HandleFunc("GET "+healthPath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	return mux
}

// newHTTPServer constructs the http.Server for the streamable-HTTP transport
// with timeouts tuned for the MCP streaming contract. It is a separate function
// so the timeout policy is unit-testable without binding a listener.
//
// Timeout rationale:
//   - ReadHeaderTimeout (10s): bounds slow-header (Slowloris) attacks.
//   - ReadTimeout (60s): bounds the full request read. Streamable-HTTP MCP
//     requests are small, quick JSON-RPC POST bodies (and bodyless GETs for the
//     server→client SSE listen stream), so 60s is generous headroom even for
//     slow clients or congestion while still adding resource-exhaustion
//     protection. The long-lived stream is the RESPONSE (governed by
//     WriteTimeout below), not the request read.
//   - IdleTimeout (120s): bounds idle keep-alive connection reuse.
//   - WriteTimeout is intentionally LEFT AT 0 (unlimited) because MCP streaming
//     responses have no upper bound on duration — any finite WriteTimeout would
//     eventually truncate a long-lived stream mid-response (even a large value
//     like 300s only defers the truncation, it does not make it safe). Do not
//     set it — that is not a bug to "fix".
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout deliberately omitted (0 = unlimited) — see doc comment.
	}
}

// ServeHTTP builds the MCP server and serves it over the streamable-HTTP
// transport on addr, blocking until ctx is cancelled. The MCP endpoint is
// mounted at /mcp and a GET /healthz liveness probe at /healthz. On ctx
// cancellation it shuts the server down gracefully with a short timeout.
func ServeHTTP(
	ctx context.Context,
	addr string,
	loaded *manifest.Loaded,
	cfg manifest.Config,
	version string,
	tracer trace.Tracer,
	stderr io.Writer,
	opts Options,
) error {
	httpSrv := newHTTPServer(addr, NewHTTPHandler(loaded, cfg, version, tracer, stderr, opts))

	if stderr != nil {
		_, _ = fmt.Fprintf(stderr, "labctl mcp: serving streamable-HTTP on %s (MCP at %s, health at %s)\n",
			addr, mcpPath, healthPath)
	}

	errCh := make(chan error, 1)
	go func() {
		err := httpSrv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("mcp http shutdown: %w", err)
		}
		return <-errCh
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("mcp http serve: %w", err)
		}
		return nil
	}
}
