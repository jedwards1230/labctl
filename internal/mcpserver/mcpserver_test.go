package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/mcpserver"
)

// buildTestLoaded creates a minimal *manifest.Loaded pointing at baseURL
// with one service ("testsvc") and one command ("ping": GET /ping).
func buildTestLoaded(baseURL string) *manifest.Loaded {
	svc := &manifest.Service{
		Name:      "testsvc",
		BaseURL:   baseURL,
		Transport: "http",
		Commands: map[string]manifest.Command{
			"ping": {
				Help:   "test ping",
				Method: "GET",
				Path:   "/ping",
			},
		},
	}
	return &manifest.Loaded{
		Config: manifest.Config{
			Secret: manifest.SecretResolver{
				Resolver: "op",
				Command:  []string{"op", "read", "{ref}"},
			},
		},
		Services: map[string]*manifest.Service{"testsvc": svc},
	}
}

// connectClientServer wires a client to a server over in-memory transport and
// returns the connected client session. The caller is responsible for closing
// the session.
func connectClientServer(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	st, ct := mcp.NewInMemoryTransports()

	_, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server Connect: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// TestToolListGeneration verifies tool registration: ignored commands are
// excluded, write commands are annotated, empty-command services produce
// nothing.
func TestToolListGeneration(t *testing.T) {
	svcA := &manifest.Service{
		Name:      "svc_a",
		BaseURL:   "http://example.com",
		Transport: "http",
		Commands: map[string]manifest.Command{
			"cmd1": {Help: "command one", Method: "GET", Path: "/one"},
			"cmd2": {Help: "command two", Method: "GET", Path: "/two", MCPIgnore: true},
		},
	}
	svcB := &manifest.Service{
		Name:      "svc_b",
		BaseURL:   "http://example.com",
		Transport: "http",
		Commands: map[string]manifest.Command{
			"cmd1": {Help: "write command", Method: "POST", Path: "/create"},
		},
	}
	svcC := &manifest.Service{
		Name:      "svc_c",
		BaseURL:   "http://example.com",
		Transport: "http",
		Commands:  map[string]manifest.Command{},
	}

	loaded := &manifest.Loaded{
		Config: manifest.Config{
			Secret: manifest.SecretResolver{Command: []string{"op", "read", "{ref}"}},
		},
		Services: map[string]*manifest.Service{
			"svc_a": svcA,
			"svc_b": svcB,
			"svc_c": svcC,
		},
	}

	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v9.9.9", tracer, nil, mcpserver.Options{})

	session := connectClientServer(t, srv)
	ctx := context.Background()

	// Collect all tools.
	toolNames := map[string]string{} // name → description
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("Tools iteration: %v", err)
		}
		toolNames[tool.Name] = tool.Description
	}

	// Should have exactly 2 tools: svc_a_cmd1 and svc_b_cmd1.
	if got := len(toolNames); got != 2 {
		t.Errorf("tool count = %d, want 2; tools: %v", got, toolNames)
	}

	if _, ok := toolNames["svc_a_cmd1"]; !ok {
		t.Error("expected tool svc_a_cmd1 not found")
	}
	if _, ok := toolNames["svc_b_cmd1"]; !ok {
		t.Error("expected tool svc_b_cmd1 not found")
	}
	// cmd2 is mcp_ignore — must not appear.
	if _, ok := toolNames["svc_a_cmd2"]; ok {
		t.Error("svc_a_cmd2 has mcp_ignore but appeared as a tool")
	}
	// svc_b_cmd1 is a POST, so Write==true and [WRITE] suffix must be present.
	if desc := toolNames["svc_b_cmd1"]; !strings.Contains(desc, "[WRITE]") {
		t.Errorf("svc_b_cmd1 description = %q, want [WRITE] suffix", desc)
	}
	// svc_a_cmd1 is a GET, no [WRITE] suffix.
	if desc := toolNames["svc_a_cmd1"]; strings.Contains(desc, "[WRITE]") {
		t.Errorf("svc_a_cmd1 description = %q, must not contain [WRITE]", desc)
	}
}

// filterLoaded builds a two-service Loaded: svc_a (one read GET) and svc_b
// (one write POST), used by the filter tests below.
func filterLoaded() *manifest.Loaded {
	svcA := &manifest.Service{
		Name:      "svc_a",
		BaseURL:   "http://example.com",
		Transport: "http",
		Commands: map[string]manifest.Command{
			"read": {Help: "a read", Method: "GET", Path: "/r"},
		},
	}
	svcB := &manifest.Service{
		Name:      "svc_b",
		BaseURL:   "http://example.com",
		Transport: "http",
		Commands: map[string]manifest.Command{
			"create": {Help: "a write", Method: "POST", Path: "/c"},
		},
	}
	return &manifest.Loaded{
		Config: manifest.Config{
			Secret: manifest.SecretResolver{Command: []string{"op", "read", "{ref}"}},
		},
		Services: map[string]*manifest.Service{"svc_a": svcA, "svc_b": svcB},
	}
}

// listToolNames connects a client and returns the set of registered tool names.
func listToolNames(t *testing.T, srv *mcp.Server) map[string]bool {
	t.Helper()
	session := connectClientServer(t, srv)
	names := map[string]bool{}
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("Tools iteration: %v", err)
		}
		names[tool.Name] = true
	}
	return names
}

// TestBuildServerReadOnly verifies --read-only drops every write tool but keeps
// reads, and that without it the write tool is present.
func TestBuildServerReadOnly(t *testing.T) {
	loaded := filterLoaded()
	tracer := noop.NewTracerProvider().Tracer("test")

	off := listToolNames(t, mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{}))
	if !off["svc_b_create"] {
		t.Error("read-only off: write tool svc_b_create should be registered")
	}

	on := listToolNames(t, mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{ReadOnly: true}))
	if on["svc_b_create"] {
		t.Error("read-only on: write tool svc_b_create must not be registered")
	}
	if !on["svc_a_read"] {
		t.Error("read-only on: read tool svc_a_read should remain")
	}
}

// TestBuildServerServiceAllowlist verifies --service exposes only the named
// service, and that it composes with --read-only.
func TestBuildServerServiceAllowlist(t *testing.T) {
	loaded := filterLoaded()
	tracer := noop.NewTracerProvider().Tracer("test")

	only := listToolNames(t, mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil,
		mcpserver.Options{Services: []string{"svc_a"}}))
	if !only["svc_a_read"] {
		t.Error("allowlist svc_a: svc_a_read should be present")
	}
	if only["svc_b_create"] {
		t.Error("allowlist svc_a: svc_b tools must be omitted")
	}

	// Allowlist svc_b but read-only → its only command is a write, so zero tools.
	both := listToolNames(t, mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil,
		mcpserver.Options{Services: []string{"svc_b"}, ReadOnly: true}))
	if len(both) != 0 {
		t.Errorf("allowlist svc_b + read-only: want 0 tools, got %v", both)
	}
}

// TestValidateServices verifies the allowlist validation: known names pass,
// unknown names produce a clear error listing the unknown name and the
// available services.
func TestValidateServices(t *testing.T) {
	loaded := filterLoaded()

	if err := mcpserver.ValidateServices(loaded, nil); err != nil {
		t.Errorf("empty allowlist should be valid, got %v", err)
	}
	if err := mcpserver.ValidateServices(loaded, []string{"svc_a"}); err != nil {
		t.Errorf("known service should be valid, got %v", err)
	}
	err := mcpserver.ValidateServices(loaded, []string{"svc_a", "nope"})
	if err == nil {
		t.Fatal("unknown service should error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "nope") {
		t.Errorf("error %q should name the unknown service 'nope'", msg)
	}
	if !strings.Contains(msg, "svc_a") || !strings.Contains(msg, "svc_b") {
		t.Errorf("error %q should list available services", msg)
	}
}

// TestToolCallDispatch verifies that a tool call reaches the HTTP endpoint and
// returns the JSON body as text content.
func TestToolCallDispatch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	}))
	defer ts.Close()

	loaded := buildTestLoaded(ts.URL)
	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v9.9.9", tracer, nil, mcpserver.Options{})
	session := connectClientServer(t, srv)

	ctx := context.Background()
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
		t.Fatal("no content in result")
	}
	txt, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] type = %T, want *mcp.TextContent", result.Content[0])
	}
	if !strings.Contains(txt.Text, "ok") {
		t.Errorf("result text = %q, want to contain \"ok\"", txt.Text)
	}
}

// TestToolCallError verifies that a 404 from the upstream produces an MCP
// error result (IsError==true) rather than a panic or a protocol error.
func TestToolCallError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ts.Close()

	loaded := buildTestLoaded(ts.URL)
	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v9.9.9", tracer, nil, mcpserver.Options{})
	session := connectClientServer(t, srv)

	ctx := context.Background()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "testsvc_ping",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool protocol error (want tool-level error): %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for 404 upstream, got false; content: %v", result.Content)
	}
}

// TestInitializeToolsListCallHandshake verifies the full MCP handshake: connect,
// list tools, then call one successfully.
func TestInitializeToolsListCallHandshake(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"pong":true}`)
	}))
	defer ts.Close()

	loaded := buildTestLoaded(ts.URL)
	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v9.9.9", tracer, nil, mcpserver.Options{})
	session := connectClientServer(t, srv)

	ctx := context.Background()

	// 1. List tools.
	var found bool
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("Tools: %v", err)
		}
		if tool.Name == "testsvc_ping" {
			found = true
		}
	}
	if !found {
		t.Fatal("testsvc_ping not found in tools list")
	}

	// 2. Call the tool.
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "testsvc_ping",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	if len(result.Content) == 0 {
		t.Fatal("no content returned")
	}
	txt, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] type = %T", result.Content[0])
	}
	// Confirm the response body made it through.
	var body map[string]any
	if err := json.Unmarshal([]byte(txt.Text), &body); err != nil {
		// The text may be pretty-printed JSON; try trimming whitespace.
		if err2 := json.Unmarshal([]byte(strings.TrimSpace(txt.Text)), &body); err2 != nil {
			t.Logf("result text: %q", txt.Text)
			// Accept it as long as it contains "pong".
			if !strings.Contains(txt.Text, "pong") {
				t.Errorf("result text = %q, want to contain \"pong\"", txt.Text)
			}
			return
		}
	}
	if pong, _ := body["pong"].(bool); !pong {
		t.Errorf("body[\"pong\"] = %v, want true", body["pong"])
	}
}
