package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/jedwards1230/labctl/internal/command"
	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/output"
)

// TestStructuredContentDegradeWhenFilteredFails covers the defensive
// degrade-to-text-only branch in executeAndRender: when the structured-content
// builder errors AFTER output.Render already succeeded, the read-tool call must
// still succeed (!IsError) with its text Content intact and StructuredContent
// left nil, logging the failure to stderr.
//
// This branch cannot be reached with a real filter: buildStructuredContent's
// output.Filtered mirrors output.Render's decode+jq path exactly, so any input
// that errors one errors the other (which would fail the whole call, not
// degrade). We therefore override the buildStructured seam to force the error —
// the only honest way to exercise an otherwise-unreachable safety net.
func TestStructuredContentDegradeWhenFilteredFails(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	}))
	t.Cleanup(ts.Close)

	orig := buildStructured
	buildStructured = func(_ *manifest.Service, _ *command.Command, _ []byte, _ manifest.Output, _ output.Options) (*structuredResult, error) {
		return nil, fmt.Errorf("forced structured failure")
	}
	t.Cleanup(func() { buildStructured = orig })

	svc := &manifest.Service{
		Name:      "testsvc",
		BaseURL:   ts.URL,
		Transport: "http",
		Commands: map[string]manifest.Command{
			"ping": {Help: "test ping", Method: "GET", Path: "/ping"},
		},
	}
	cmds := command.FromManifest(svc)
	c := cmds["ping"]
	if c == nil {
		t.Fatal("ping command not produced by FromManifest")
	}
	if c.Write {
		t.Fatal("ping must be a read command for the structured branch to run")
	}

	var stderr strings.Builder
	tracer := noop.NewTracerProvider().Tracer("test")
	cfg := manifest.Config{Secret: manifest.SecretResolver{Command: []string{"op", "read", "{ref}"}}}

	result := executeAndRender(context.Background(), svc, c, nil, "", false, cfg, tracer, &stderr)

	if result.IsError {
		t.Fatalf("call must still succeed when the structured builder fails; got error: %v", result.Content)
	}
	if result.StructuredContent != nil {
		t.Errorf("StructuredContent = %v, want nil (degraded to text-only)", result.StructuredContent)
	}
	if len(result.Content) == 0 {
		t.Fatal("text Content must remain populated after degradation")
	}
	txt, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] type = %T, want *mcp.TextContent", result.Content[0])
	}
	if !strings.Contains(txt.Text, "ok") {
		t.Errorf("text Content = %q, want to contain the rendered body \"ok\"", txt.Text)
	}
	if !strings.Contains(stderr.String(), "structured content") {
		t.Errorf("stderr = %q, want a logged structured-content degradation note", stderr.String())
	}
}
