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
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           NewHTTPHandler(loaded, cfg, version, tracer, stderr, opts),
		ReadHeaderTimeout: 10 * time.Second,
	}

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
