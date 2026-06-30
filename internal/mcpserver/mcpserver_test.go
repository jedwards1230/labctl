package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	// The named-command tools svc_a_cmd1 and svc_b_cmd1 are present; svc_a_cmd2
	// (mcp_ignore) is not. Generic verb tools (svc_*_get/_post/…) are also now
	// registered per service, so this no longer asserts an exact total.
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

	// Allowlist svc_b but read-only → its only named command is a write (dropped),
	// and the only generic verb that survives read-only is the read `svc_b_get`.
	// No svc_a tools leak in, and no svc_b write verbs appear.
	both := listToolNames(t, mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil,
		mcpserver.Options{Services: []string{"svc_b"}, ReadOnly: true}))
	if both["svc_b_create"] {
		t.Error("allowlist svc_b + read-only: write named command svc_b_create must be omitted")
	}
	if !both["svc_b_get"] {
		t.Error("allowlist svc_b + read-only: generic read verb svc_b_get should remain")
	}
	for _, w := range []string{"svc_b_post", "svc_b_put", "svc_b_patch", "svc_b_delete"} {
		if both[w] {
			t.Errorf("allowlist svc_b + read-only: write verb %q must be omitted", w)
		}
	}
	for name := range both {
		if strings.HasPrefix(name, "svc_a_") {
			t.Errorf("allowlist svc_b: svc_a tool %q leaked", name)
		}
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

// verbLoaded builds a Loaded with one http service ("authentik") and one
// jsonrpc-ws service ("truenas"), each carrying a single read command. Used by
// the generic-verb registration tests.
func verbLoaded() *manifest.Loaded {
	httpSvc := &manifest.Service{
		Name:      "authentik",
		BaseURL:   "http://example.com",
		Transport: "http",
		Commands: map[string]manifest.Command{
			"users": {Help: "list users", Method: "GET", Path: "/api/v3/core/users/"},
		},
	}
	wsSvc := &manifest.Service{
		Name:      "truenas",
		BaseURL:   "ws://example.com/websocket",
		Transport: "jsonrpc-ws",
		Commands: map[string]manifest.Command{
			"info": {Help: "system info", Method: "system.info"},
		},
	}
	return &manifest.Loaded{
		Config: manifest.Config{
			Secret: manifest.SecretResolver{Command: []string{"op", "read", "{ref}"}},
		},
		Services: map[string]*manifest.Service{"authentik": httpSvc, "truenas": wsSvc},
	}
}

// TestVerbToolRegistration verifies the generic verbs are exposed as per-service
// MCP tools: every http verb (minus head) for an http service and `call` for a
// jsonrpc-ws service when writes are allowed, and only the read tools under
// read-only.
func TestVerbToolRegistration(t *testing.T) {
	loaded := verbLoaded()
	tracer := noop.NewTracerProvider().Tracer("test")

	t.Run("writes allowed", func(t *testing.T) {
		names := listToolNames(t, mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{}))
		want := []string{
			"authentik_get", "authentik_post", "authentik_put", "authentik_patch", "authentik_delete",
			"truenas_call",
		}
		for _, n := range want {
			if !names[n] {
				t.Errorf("expected verb tool %q, got %v", n, names)
			}
		}
		// HEAD is intentionally not exposed.
		if names["authentik_head"] {
			t.Error("authentik_head must not be registered")
		}
		// jsonrpc-ws services get `call`, not the http verbs.
		if names["truenas_get"] || names["truenas_post"] {
			t.Errorf("jsonrpc-ws service must not get http verb tools, got %v", names)
		}
	})

	t.Run("read-only", func(t *testing.T) {
		names := listToolNames(t, mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{ReadOnly: true}))
		if !names["authentik_get"] {
			t.Error("read-only: authentik_get (a read) should remain")
		}
		for _, n := range []string{"authentik_post", "authentik_put", "authentik_patch", "authentik_delete"} {
			if names[n] {
				t.Errorf("read-only: write verb %q must not be registered", n)
			}
		}
		// `call`'s write-ness is unknown statically, so it's treated as a write.
		if names["truenas_call"] {
			t.Error("read-only: truenas_call (treated as a write) must not be registered")
		}
	})
}

// TestVerbDescriptionsAndAnnotations verifies write verbs are flagged MUTATING
// in the prose and that annotations follow the method's destructive/idempotent
// hints.
func TestVerbDescriptionsAndAnnotations(t *testing.T) {
	loaded := verbLoaded()
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

	get := tools["authentik_get"]
	if get == nil {
		t.Fatal("authentik_get missing")
	}
	if strings.Contains(get.Description, "MUTATING") {
		t.Errorf("get description must not be MUTATING: %q", get.Description)
	}
	if get.Annotations == nil || !get.Annotations.ReadOnlyHint {
		t.Error("get must have ReadOnlyHint=true")
	}

	del := tools["authentik_delete"]
	if del == nil {
		t.Fatal("authentik_delete missing")
	}
	if !strings.Contains(del.Description, "MUTATING") {
		t.Errorf("delete description must be MUTATING: %q", del.Description)
	}
	if del.Annotations == nil || del.Annotations.DestructiveHint == nil || !*del.Annotations.DestructiveHint {
		t.Error("delete must have DestructiveHint=true")
	}
	if !del.Annotations.IdempotentHint {
		t.Error("delete must have IdempotentHint=true")
	}

	post := tools["authentik_post"]
	if post == nil {
		t.Fatal("authentik_post missing")
	}
	if post.Annotations == nil || post.Annotations.DestructiveHint == nil || *post.Annotations.DestructiveHint {
		t.Error("post must have DestructiveHint=false")
	}

	call := tools["truenas_call"]
	if call == nil {
		t.Fatal("truenas_call missing")
	}
	if !strings.Contains(call.Description, "MUTATING") {
		t.Errorf("call description must be MUTATING: %q", call.Description)
	}
	// call's method is unknown → default branch leaves DestructiveHint unset.
	if call.Annotations != nil && call.Annotations.DestructiveHint != nil {
		t.Errorf("call must leave DestructiveHint unset, got %v", *call.Annotations.DestructiveHint)
	}
}

// TestVerbSchemaRequiredFields verifies the verb input schemas mark path/method
// required and carry the universal filter/raw flags.
func TestVerbSchemaRequiredFields(t *testing.T) {
	loaded := verbLoaded()
	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{})

	session := connectClientServer(t, srv)
	schemas := map[string]json.RawMessage{}
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("Tools iteration: %v", err)
		}
		b, _ := json.Marshal(tool.InputSchema)
		schemas[tool.Name] = b
	}

	type sch struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}

	cases := []struct {
		tool         string
		wantReq      string
		wantProps    []string
		notWantProps []string
	}{
		{"authentik_get", "path", []string{"path", "query", "filter", "raw"}, []string{"body", "method"}},
		{"authentik_post", "path", []string{"path", "body", "filter", "raw"}, []string{"query", "method"}},
		{"authentik_delete", "path", []string{"path", "filter", "raw"}, []string{"body", "method"}},
		{"truenas_call", "method", []string{"method", "params", "filter", "raw"}, []string{"path"}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			raw, ok := schemas[tc.tool]
			if !ok {
				t.Fatalf("schema for %q missing", tc.tool)
			}
			var s sch
			if err := json.Unmarshal(raw, &s); err != nil {
				t.Fatalf("unmarshal schema: %v", err)
			}
			if len(s.Required) != 1 || s.Required[0] != tc.wantReq {
				t.Errorf("required = %v, want [%s]", s.Required, tc.wantReq)
			}
			for _, p := range tc.wantProps {
				if _, ok := s.Properties[p]; !ok {
					t.Errorf("missing property %q in %v", p, s.Properties)
				}
			}
			for _, p := range tc.notWantProps {
				if _, ok := s.Properties[p]; ok {
					t.Errorf("unexpected property %q in %v", p, s.Properties)
				}
			}
		})
	}
}

// TestVerbNameCollisionGuard verifies that a manifest command literally named
// `get` wins, so no duplicate generic `<svc>_get` is registered and BuildServer
// does not panic on a would-be duplicate AddTool.
func TestVerbNameCollisionGuard(t *testing.T) {
	svc := &manifest.Service{
		Name:      "svc",
		BaseURL:   "http://example.com",
		Transport: "http",
		Commands: map[string]manifest.Command{
			// A named command whose id collides with the generic GET verb.
			"get": {Help: "named get", Method: "GET", Path: "/named"},
		},
	}
	loaded := &manifest.Loaded{
		Config: manifest.Config{
			Secret: manifest.SecretResolver{Command: []string{"op", "read", "{ref}"}},
		},
		Services: map[string]*manifest.Service{"svc": svc},
	}
	tracer := noop.NewTracerProvider().Tracer("test")

	var srv *mcp.Server
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("BuildServer panicked on verb/command name collision: %v", r)
			}
		}()
		srv = mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{})
	}()

	session := connectClientServer(t, srv)
	var getTools []string
	var getDesc string
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("Tools iteration: %v", err)
		}
		if tool.Name == "svc_get" {
			getTools = append(getTools, tool.Name)
			getDesc = tool.Description
		}
	}
	if len(getTools) != 1 {
		t.Fatalf("svc_get registered %d times, want exactly 1", len(getTools))
	}
	// The named command (help "named get") must win, not the generic verb.
	if !strings.Contains(getDesc, "named get") {
		t.Errorf("svc_get description = %q, want the named command to win", getDesc)
	}
	// The other http verbs are still added generically.
	names := listToolNames(t, srv)
	if !names["svc_post"] {
		t.Error("svc_post should still be registered alongside the named get")
	}
}

// TestVerbToolCallDispatch verifies a generic-verb tool call round-trips to the
// HTTP endpoint with the right method, path, and body, returning the response
// body as text content.
func TestVerbToolCallDispatch(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"created":true}`)
	}))
	defer ts.Close()

	loaded := buildTestLoaded(ts.URL)
	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v9.9.9", tracer, nil, mcpserver.Options{})
	session := connectClientServer(t, srv)

	ctx := context.Background()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "testsvc_post",
		Arguments: map[string]any{
			"path": "/widgets",
			"body": `{"name":"gadget"}`,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %v", result.Content)
	}
	if gotMethod != "POST" {
		t.Errorf("upstream method = %q, want POST", gotMethod)
	}
	if gotPath != "/widgets" {
		t.Errorf("upstream path = %q, want /widgets", gotPath)
	}
	if !strings.Contains(gotBody, "gadget") {
		t.Errorf("upstream body = %q, want it to contain gadget", gotBody)
	}
	if len(result.Content) == 0 {
		t.Fatal("no content in result")
	}
	txt, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] type = %T, want *mcp.TextContent", result.Content[0])
	}
	if !strings.Contains(txt.Text, "created") {
		t.Errorf("result text = %q, want to contain \"created\"", txt.Text)
	}
}

// TestVerbToolCallMissingPath verifies a generic-verb call with no path returns
// a tool-level error (not a panic, not a protocol error).
func TestVerbToolCallMissingPath(t *testing.T) {
	loaded := buildTestLoaded("http://example.com")
	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{})
	session := connectClientServer(t, srv)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "testsvc_get",
		Arguments: map[string]any{}, // no path
	})
	if err != nil {
		t.Fatalf("CallTool protocol error (want tool-level error): %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for missing path, got false; content: %v", result.Content)
	}
}

// TestToolNamesIndependentOfCLITree pins the MCP naming contract: tool names are
// derived directly from the loaded manifests as <service>_<command> and are
// unaffected by where the CLI registers service commands in the cobra tree.
// When services moved from the root to the `svc` parent (a CLI-only change),
// these names must NOT change — agents wired to `radarr_list`/`tdarr_status`
// keep working.
func TestToolNamesIndependentOfCLITree(t *testing.T) {
	loaded := &manifest.Loaded{
		Config: manifest.Config{
			Secret: manifest.SecretResolver{Command: []string{"op", "read", "{ref}"}},
		},
		Services: map[string]*manifest.Service{
			"radarr": {
				Name:      "radarr",
				BaseURL:   "http://example.com",
				Transport: "http",
				Commands: map[string]manifest.Command{
					"list": {Help: "list movies", Method: "GET", Path: "/api/v3/movie"},
				},
			},
			"tdarr": {
				Name:      "tdarr",
				BaseURL:   "http://example.com",
				Transport: "http",
				Commands: map[string]manifest.Command{
					"status": {Help: "node status", Method: "GET", Path: "/api/v2/status"},
				},
			},
		},
	}

	tracer := noop.NewTracerProvider().Tracer("test")
	srv := mcpserver.BuildServer(loaded, loaded.Config, "v0", tracer, nil, mcpserver.Options{})
	names := listToolNames(t, srv)

	for _, want := range []string{"radarr_list", "tdarr_status"} {
		if !names[want] {
			t.Errorf("missing MCP tool %q; svc-namespace refactor must not change tool names (got %v)", want, names)
		}
	}
	// And the `svc` prefix from the CLI tree must never leak into a tool name.
	for name := range names {
		if strings.HasPrefix(name, "svc_") {
			t.Errorf("tool %q leaked the CLI `svc` parent into its name", name)
		}
	}
}
