package mcpserver_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/jedwards1230/labctl/internal/mcpserver"
)

// TestHTTPHandlerRoundTrip stands up the streamable-HTTP handler in front of an
// httptest server and drives a full MCP round-trip with the SDK's streamable
// client transport: initialize (via Connect), tools/list, and a tool call. It
// also asserts GET /healthz returns 200.
func TestHTTPHandlerRoundTrip(t *testing.T) {
	// Upstream the manifest's single command targets.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	loaded := buildTestLoaded(upstream.URL)
	tracer := noop.NewTracerProvider().Tracer("test")
	handler := mcpserver.NewHTTPHandler(loaded, loaded.Config, "v9.9.9", tracer, nil, mcpserver.Options{})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// healthz must answer 200.
	t.Run("healthz", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/healthz")
		if err != nil {
			t.Fatalf("GET /healthz: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET /healthz status = %d, want 200", resp.StatusCode)
		}
	})

	// Full MCP round-trip over the streamable-HTTP transport.
	t.Run("mcp_round_trip", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		transport := &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}
		client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
		session, err := client.Connect(ctx, transport, nil)
		if err != nil {
			t.Fatalf("client Connect (initialize): %v", err)
		}
		defer func() { _ = session.Close() }()

		// tools/list: the test manifest exposes exactly testsvc_ping.
		var found bool
		for tool, err := range session.Tools(ctx, nil) {
			if err != nil {
				t.Fatalf("Tools iteration: %v", err)
			}
			if tool.Name == "testsvc_ping" {
				found = true
			}
		}
		if !found {
			t.Fatal("testsvc_ping not present in tools/list over streamable-HTTP")
		}

		// tools/call: round-trips through engine.Execute to the upstream.
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "testsvc_ping",
			Arguments: map[string]any{},
		})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if result.IsError {
			t.Fatalf("tool returned error: %v", result.Content)
		}
		if len(result.Content) == 0 {
			t.Fatal("no content in tool result")
		}
		txt, ok := result.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("content[0] type = %T, want *mcp.TextContent", result.Content[0])
		}
		if !strings.Contains(txt.Text, "ok") {
			t.Errorf("result text = %q, want to contain \"ok\"", txt.Text)
		}
	})
}

// TestServeHTTPLifecycle starts ServeHTTP on an ephemeral port, waits for the
// health endpoint to answer, then cancels the context and asserts a clean
// (nil) graceful shutdown.
func TestServeHTTPLifecycle(t *testing.T) {
	addr := freeAddr(t)
	loaded := buildTestLoaded("http://127.0.0.1:1") // upstream irrelevant; no tool call here
	tracer := noop.NewTracerProvider().Tracer("test")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- mcpserver.ServeHTTP(ctx, addr, loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{})
	}()

	// Wait for the listener to come up via /healthz.
	healthURL := "http://" + addr + "/healthz"
	if !waitForHealth(t, healthURL) {
		cancel()
		<-errCh
		t.Fatal("server did not become healthy in time")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("ServeHTTP returned error on graceful shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServeHTTP did not return after context cancel")
	}
}

// freeAddr returns a currently-free 127.0.0.1 host:port by binding :0 and
// releasing it. There is an inherent (small) race before ServeHTTP rebinds.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// waitForHealth polls the health URL until it answers 200 or a deadline passes.
func waitForHealth(t *testing.T, url string) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
