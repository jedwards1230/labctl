package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/mcpserver"
)

// syncBuffer is a race-safe io.Writer: the MCP server writes the audit record
// from its own goroutine while the test reads it after CallTool returns.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestStructuredTypedErrorResult proves an HTTP≥400 upstream produces a
// STRUCTURED error result: IsError=true, the {error,class,status} object in
// StructuredContent, AND the unchanged err.Error() text fallback (item 9).
func TestStructuredTypedErrorResult(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // 404 → *transport.HTTPError, class "http"
	}))
	defer ts.Close()

	loaded := buildTestLoaded(ts.URL)
	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{})
	session := connectClientServer(t, srv)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "testsvc_ping",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool protocol error (want tool-level error): %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for a 404 upstream")
	}
	// Text fallback is unchanged (the raw error string).
	if len(result.Content) == 0 {
		t.Fatal("expected text content fallback alongside the structured error")
	}
	txt, ok := result.Content[0].(*mcp.TextContent)
	if !ok || txt.Text == "" {
		t.Fatalf("content[0] = %T, want non-empty *mcp.TextContent", result.Content[0])
	}
	// Structured error object carries the typed class + status.
	sc, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("StructuredContent type = %T, want map[string]any", result.StructuredContent)
	}
	if sc["class"] != "http" {
		t.Errorf("class = %v, want http", sc["class"])
	}
	if sc["status"] != float64(404) {
		t.Errorf("status = %v, want 404", sc["status"])
	}
	if sc["error"] != txt.Text {
		t.Errorf("structured error %q != text fallback %q", sc["error"], txt.Text)
	}
}

// TestUsageClassOnBadVerbInput proves malformed tool input (a generic verb with
// no path) classifies as "usage" through the same structured error path.
func TestUsageClassOnBadVerbInput(t *testing.T) {
	loaded := buildTestLoaded("http://127.0.0.1:0")
	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{})
	session := connectClientServer(t, srv)

	// testsvc_get with no path → command.Verb usage error.
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "testsvc_get",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for missing path")
	}
	sc, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("StructuredContent type = %T, want map[string]any", result.StructuredContent)
	}
	if sc["class"] != "usage" {
		t.Errorf("class = %v, want usage", sc["class"])
	}
}

// TestDryRunToolInputSkipsNetwork proves a write tool called with dry_run:true
// returns the preview text and never hits the network (item 10). The upstream
// handler fails the test if it is ever reached.
func TestDryRunToolInputSkipsNetwork(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream must NOT be called during a dry-run (got %s %s)", r.Method, r.URL.Path)
		http.Error(w, "should not happen", http.StatusInternalServerError)
	}))
	defer ts.Close()

	loaded := &manifest.Loaded{
		Config: manifest.Config{
			Secret: manifest.SecretResolver{Command: []string{"op", "read", "{ref}"}},
		},
		Services: map[string]*manifest.Service{
			"svc": {
				Name:      "svc",
				BaseURL:   ts.URL,
				Transport: "http",
				Commands: map[string]manifest.Command{
					"create": {Help: "create a thing", Method: "POST", Path: "/c"},
				},
			},
		},
	}
	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{})
	session := connectClientServer(t, srv)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "svc_create",
		Arguments: map[string]any{"dry_run": true},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("dry-run returned error: %v", result.Content)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected preview text content")
	}
	txt, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *mcp.TextContent", result.Content[0])
	}
	if !strings.Contains(txt.Text, "POST "+ts.URL+"/c") {
		t.Errorf("preview text = %q, want the POST request line", txt.Text)
	}
	// Dry-run stays text-only (no structured content).
	if result.StructuredContent != nil {
		t.Errorf("dry-run StructuredContent = %v, want nil", result.StructuredContent)
	}
}

// TestMutationAuditRecordEmittedOnWrite proves the MCP path emits one structured
// audit record per write call (item 11) — here a write dry-run yields an
// outcome:"dry-run" record on stderr. Reads emit nothing.
func TestMutationAuditRecordEmittedOnWrite(t *testing.T) {
	ts := jsonServer(t, `{"ok":true}`)
	loaded := &manifest.Loaded{
		Config: manifest.Config{
			Secret: manifest.SecretResolver{Command: []string{"op", "read", "{ref}"}},
		},
		Services: map[string]*manifest.Service{
			"svc": {
				Name:      "svc",
				BaseURL:   ts.URL,
				Transport: "http",
				Commands: map[string]manifest.Command{
					"create": {Help: "create a thing", Method: "POST", Path: "/c"},
					"read":   {Help: "read a thing", Method: "GET", Path: "/r"},
				},
			},
		},
	}
	var stderr syncBuffer
	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, &stderr, mcpserver.Options{})
	session := connectClientServer(t, srv)

	// A read call must NOT emit an audit record.
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "svc_read", Arguments: map[string]any{},
	}); err != nil {
		t.Fatalf("read CallTool: %v", err)
	}
	if s := stderr.String(); strings.Contains(s, `"outcome"`) {
		t.Fatalf("read call emitted an audit record: %q", s)
	}

	// A write dry-run must emit exactly one outcome:"dry-run" record.
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "svc_create", Arguments: map[string]any{"dry_run": true},
	}); err != nil {
		t.Fatalf("write CallTool: %v", err)
	}

	line := strings.TrimSpace(stderr.String())
	if line == "" {
		t.Fatal("expected an audit record on the write call, got none")
	}
	var rec struct {
		Service string `json:"service"`
		Command string `json:"command"`
		Method  string `json:"method"`
		DryRun  bool   `json:"dry_run"`
		Outcome string `json:"outcome"`
		Caller  string `json:"caller"`
	}
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("audit record not valid JSON: %v (%q)", err, line)
	}
	if rec.Service != "svc" || rec.Command != "create" || rec.Method != "POST" {
		t.Errorf("record = %+v, want service=svc command=create method=POST", rec)
	}
	if !rec.DryRun || rec.Outcome != "dry-run" {
		t.Errorf("record = %+v, want dry_run=true outcome=dry-run", rec)
	}
	if rec.Caller != "unknown" {
		t.Errorf("caller = %q, want unknown (reserved field)", rec.Caller)
	}
}
