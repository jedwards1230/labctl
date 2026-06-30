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
	"github.com/jedwards1230/labctl/internal/mcpserver/views"
)

const resultResourceURI = "ui://labctl/result"

// toolMetaResourceURI extracts _meta.ui.resourceUri from a tool's Meta map (a
// map[string]any after the JSON round-trip over the in-memory transport), or
// "" with ok=false when absent/malformed.
func toolMetaResourceURI(t *testing.T, meta mcp.Meta) (string, bool) {
	t.Helper()
	if meta == nil {
		return "", false
	}
	uiAny, ok := meta["ui"]
	if !ok {
		return "", false
	}
	ui, ok := uiAny.(map[string]any)
	if !ok {
		t.Fatalf("_meta.ui type = %T, want map[string]any", uiAny)
	}
	uri, ok := ui["resourceUri"].(string)
	return uri, ok
}

// TestUIMetaOnReadAndWriteTools proves the MCP Apps wiring contract: every
// read tool (named command and the generic GET verb) carries
// _meta.ui.resourceUri == ui://labctl/result, and no write tool (named POST
// command or any generic write verb) carries it.
func TestUIMetaOnReadAndWriteTools(t *testing.T) {
	loaded := filterLoaded() // svc_a: read "read" (GET /r); svc_b: write "create" (POST /c)
	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{})

	session := connectClientServer(t, srv)
	tools := map[string]*mcp.Tool{}
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("Tools iteration: %v", err)
		}
		tools[tool.Name] = tool
	}

	readTools := []string{"svc_a_read", "svc_a_get"}
	for _, name := range readTools {
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("expected tool %q not found", name)
		}
		uri, ok := toolMetaResourceURI(t, tool.Meta)
		if !ok || uri != resultResourceURI {
			t.Errorf("%s: _meta.ui.resourceUri = %q (ok=%v), want %q", name, uri, ok, resultResourceURI)
		}
	}

	writeTools := []string{"svc_b_create", "svc_a_post", "svc_a_put", "svc_a_patch", "svc_a_delete", "svc_b_post"}
	for _, name := range writeTools {
		tool, ok := tools[name]
		if !ok {
			// Not every write verb necessarily exists for every service in this
			// fixture; skip names that weren't registered.
			continue
		}
		if uri, ok := toolMetaResourceURI(t, tool.Meta); ok {
			t.Errorf("%s: write tool must not carry _meta.ui.resourceUri, got %q", name, uri)
		}
	}
}

// TestResultResourceRegisteredAndReadable proves the single universal result
// View resource is registered once on the server (independent of how many
// services/commands are loaded) and is readable, returning the embedded HTML
// under the documented MIME type.
func TestResultResourceRegisteredAndReadable(t *testing.T) {
	loaded := filterLoaded()
	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{})

	session := connectClientServer(t, srv)
	ctx := context.Background()

	var found *mcp.Resource
	for res, err := range session.Resources(ctx, nil) {
		if err != nil {
			t.Fatalf("Resources iteration: %v", err)
		}
		if res.URI == resultResourceURI {
			found = res
		}
	}
	if found == nil {
		t.Fatal("ui://labctl/result not found in resources/list")
	}
	if found.MIMEType != views.ResultMIMEType {
		t.Errorf("resource MIMEType = %q, want %q", found.MIMEType, views.ResultMIMEType)
	}

	read, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: resultResourceURI})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(read.Contents) != 1 {
		t.Fatalf("ReadResource contents = %d, want 1", len(read.Contents))
	}
	got := read.Contents[0].Text
	want := string(views.ResultHTML())
	if got != want {
		t.Errorf("resource text = %q, want embedded result.html %q", got, want)
	}
	if read.Contents[0].MIMEType != views.ResultMIMEType {
		t.Errorf("resource content MIMEType = %q, want %q", read.Contents[0].MIMEType, views.ResultMIMEType)
	}
}

// jsonServer starts an httptest server that always responds with body under
// Content-Type: application/json.
func jsonServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// structuredEnvelope mirrors the wrapper shape for test assertions:
// { "result": ..., "labctl": { "service", "command", "title", "ui" } }.
type structuredEnvelope struct {
	Result any `json:"result"`
	Labctl struct {
		Service string `json:"service"`
		Command string `json:"command"`
		Title   string `json:"title"`
		UI      any    `json:"ui"`
	} `json:"labctl"`
}

// decodeStructured re-marshals a CallToolResult.StructuredContent (an `any`,
// map[string]any after the client-side JSON round-trip) into the typed
// envelope above for assertions.
func decodeStructured(t *testing.T, sc any) structuredEnvelope {
	t.Helper()
	b, err := json.Marshal(sc)
	if err != nil {
		t.Fatalf("marshal StructuredContent: %v", err)
	}
	var env structuredEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("unmarshal StructuredContent into envelope: %v\nraw: %s", err, b)
	}
	return env
}

// TestStructuredContentWrapperShapeOnReadTool proves a read-tool call
// populates StructuredContent with the documented object-root wrapper
// (result + labctl.service/command/title/ui) ALONGSIDE the unchanged text
// content, and that result is the SAME value the text rendering is derived
// from.
func TestStructuredContentWrapperShapeOnReadTool(t *testing.T) {
	ts := jsonServer(t, `{"status":"ok","n":2}`)
	loaded := buildTestLoaded(ts.URL) // testsvc_ping: GET /ping, help "test ping"
	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v9.9.9", tracer, nil, mcpserver.Options{})
	session := connectClientServer(t, srv)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "testsvc_ping",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %v", result.Content)
	}
	// Text content is unchanged/still present (additive, not replaced).
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty Content (text fallback) alongside StructuredContent")
	}
	if result.StructuredContent == nil {
		t.Fatal("expected StructuredContent on a read-tool call, got nil")
	}

	env := decodeStructured(t, result.StructuredContent)
	wantResult := map[string]any{"status": "ok", "n": float64(2)}
	gotResult, ok := env.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", env.Result)
	}
	if gotResult["status"] != wantResult["status"] || gotResult["n"] != wantResult["n"] {
		t.Errorf("result = %#v, want %#v", gotResult, wantResult)
	}
	if env.Labctl.Service != "testsvc" {
		t.Errorf("labctl.service = %q, want testsvc", env.Labctl.Service)
	}
	if env.Labctl.Command != "ping" {
		t.Errorf("labctl.command = %q, want ping", env.Labctl.Command)
	}
	if env.Labctl.Title == "" {
		t.Error("labctl.title must not be empty")
	}
	if env.Labctl.UI != nil {
		t.Errorf("labctl.ui = %v, want nil (no manifest ui: block)", env.Labctl.UI)
	}
}

// TestStructuredContentAbsentOnWriteTool proves a write tool's result stays
// text-only — no StructuredContent — per the Phase 1+2 contract.
func TestStructuredContentAbsentOnWriteTool(t *testing.T) {
	ts := jsonServer(t, `{"created":true}`)
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
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %v", result.Content)
	}
	if result.StructuredContent != nil {
		t.Errorf("write tool StructuredContent = %v, want nil", result.StructuredContent)
	}
}

// TestStructuredContentRawFlag proves the raw flag's result is the decoded
// raw response body (no double-encoding) rather than the jq-filtered value.
func TestStructuredContentRawFlag(t *testing.T) {
	ts := jsonServer(t, `{"status":"ok","nested":{"a":1}}`)
	loaded := buildTestLoaded(ts.URL)
	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{})
	session := connectClientServer(t, srv)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "testsvc_ping",
		Arguments: map[string]any{"raw": true},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %v", result.Content)
	}
	env := decodeStructured(t, result.StructuredContent)
	got, ok := env.Result.(map[string]any)
	if !ok {
		t.Fatalf("raw result type = %T, want map[string]any", env.Result)
	}
	nested, ok := got["nested"].(map[string]any)
	if !ok || nested["a"] != float64(1) {
		t.Errorf("raw result = %#v, want the full decoded body (incl. nested)", got)
	}
}

// TestStructuredContentCarriesManifestUIHints proves a command's ui: block
// (threaded through command.FromManifest) ends up verbatim in
// structuredContent.labctl.ui.
func TestStructuredContentCarriesManifestUIHints(t *testing.T) {
	ts := jsonServer(t, `[{"id":1,"name":"a"},{"id":2,"name":"b"}]`)
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
					"list": {
						Help:   "list things",
						Method: "GET",
						Path:   "/list",
						UI: manifest.UI{
							View:    "table",
							Columns: []string{"id", "name"},
							Sort:    &manifest.UISort{By: "id", Dir: "desc"},
						},
					},
				},
			},
		},
	}
	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{})
	session := connectClientServer(t, srv)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "svc_list",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %v", result.Content)
	}
	env := decodeStructured(t, result.StructuredContent)
	ui, ok := env.Labctl.UI.(map[string]any)
	if !ok {
		t.Fatalf("labctl.ui type = %T, want map[string]any", env.Labctl.UI)
	}
	if ui["view"] != "table" {
		t.Errorf("labctl.ui.view = %v, want table", ui["view"])
	}
	cols, ok := ui["columns"].([]any)
	if !ok || len(cols) != 2 || cols[0] != "id" || cols[1] != "name" {
		t.Errorf("labctl.ui.columns = %v, want [id name]", ui["columns"])
	}
	sort, ok := ui["sort"].(map[string]any)
	if !ok || sort["by"] != "id" || sort["dir"] != "desc" {
		t.Errorf("labctl.ui.sort = %v, want {by:id dir:desc}", ui["sort"])
	}
}

// loadedWithOutput builds a one-service ("testsvc") / one-command ("ping": GET
// /ping) *manifest.Loaded pointing at baseURL, with the command's Output set to
// out — so a test can exercise a default_filter / mode without touching the
// shared buildTestLoaded fixture.
func loadedWithOutput(baseURL string, out manifest.Output) *manifest.Loaded {
	return &manifest.Loaded{
		Config: manifest.Config{
			Secret: manifest.SecretResolver{Command: []string{"op", "read", "{ref}"}},
		},
		Services: map[string]*manifest.Service{
			"testsvc": {
				Name:      "testsvc",
				BaseURL:   baseURL,
				Transport: "http",
				Commands: map[string]manifest.Command{
					"ping": {Help: "test ping", Method: "GET", Path: "/ping", Output: out},
				},
			},
		},
	}
}

// TestStructuredContentResultMatchesTextRendering is the end-to-end counterpart
// to output_test's TestFilteredMatchesRenderSingleResult: it locks the contract
// (mcpserver.go's buildStructuredContent) that StructuredContent.result is the
// SAME value the human-facing text Content is derived from, all the way through
// a real MCP tool call. It exercises default_filter, a --filter override, scalar
// mode, and --raw — the four flag paths most at risk of Render/Filtered drift —
// and asserts the text output, parsed to JSON, marshals identically to
// StructuredContent.result. Without this, a future refactor could let agents see
// inconsistent text vs structured data without any test failing.
func TestStructuredContentResultMatchesTextRendering(t *testing.T) {
	cases := []struct {
		name string
		body string
		out  manifest.Output
		args map[string]any
	}{
		{"default filter", `{"items":[{"id":1},{"id":2}],"total":2}`, manifest.Output{DefaultFilter: ".items"}, map[string]any{}},
		{"filter override", `{"items":[{"id":1},{"id":2}],"total":2}`, manifest.Output{DefaultFilter: ".items"}, map[string]any{"filter": ".total"}},
		{"scalar mode", `{"version":"6.0.4"}`, manifest.Output{DefaultFilter: ".version", Mode: "scalar"}, map[string]any{}},
		{"raw flag", `{"status":"ok","nested":{"a":1}}`, manifest.Output{DefaultFilter: ".status"}, map[string]any{"raw": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := jsonServer(t, tc.body)
			loaded := loadedWithOutput(ts.URL, tc.out)
			tracer := noop.NewTracerProvider().Tracer("test")
			srv := mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{})
			session := connectClientServer(t, srv)

			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "testsvc_ping",
				Arguments: tc.args,
			})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if result.IsError {
				t.Fatalf("tool returned error: %v", result.Content)
			}
			if result.StructuredContent == nil {
				t.Fatal("expected StructuredContent on a read-tool call, got nil")
			}
			if len(result.Content) == 0 {
				t.Fatal("expected non-empty text Content alongside StructuredContent")
			}
			txt, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("content[0] type = %T, want *mcp.TextContent", result.Content[0])
			}

			// Derive the value the text path represents: parse it as JSON, falling
			// back to the literal string for scalar mode's bare (non-JSON) output —
			// exactly as TestFilteredMatchesRenderSingleResult does.
			wantText := strings.TrimRight(txt.Text, "\n")
			var textVal any
			if jsonErr := json.Unmarshal([]byte(wantText), &textVal); jsonErr != nil {
				textVal = wantText
			}
			textJSON, err := json.Marshal(textVal)
			if err != nil {
				t.Fatalf("marshal text-derived value: %v", err)
			}

			env := decodeStructured(t, result.StructuredContent)
			resultJSON, err := json.Marshal(env.Result)
			if err != nil {
				t.Fatalf("marshal StructuredContent.result: %v", err)
			}
			if string(textJSON) != string(resultJSON) {
				t.Errorf("StructuredContent.result = %s, want (derived from text Content) %s", resultJSON, textJSON)
			}
		})
	}
}
