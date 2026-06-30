// Package mcpserver exposes loaded manifests as MCP tools over stdio or
// streamable-HTTP. Each non-ignored command in each service becomes one tool
// named <service>_<command>. All tool calls dispatch through engine.Execute —
// the same path as the CLI — so behaviour is identical from both faces and from
// both transports.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/jedwards1230/labctl/internal/command"
	"github.com/jedwards1230/labctl/internal/engine"
	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/output"
)

// argRe finds {arg.N} and {argN} placeholders in a template string.
var argRe = regexp.MustCompile(`\{arg\.?(\d+)\}`)

// scanArgIndices returns the larger of max and the highest arg index referenced
// in s. Reused across every templated field so the schema and call-time arg
// extraction agree on the arg count.
func scanArgIndices(s string, max int) int {
	for _, m := range argRe.FindAllStringSubmatch(s, -1) {
		n, err := strconv.Atoi(m[1])
		if err == nil && n > max {
			max = n
		}
	}
	return max
}

// maxArgIndex returns the highest arg index referenced in all template fields of
// a command, or -1 if none exist. It covers the http fields (Path/Query/Body),
// the jsonrpc-ws Params, and each pipeline step's templated Path/Query/Body —
// but NOT the jq fields (extract/when/body_transform), which are jq, not
// templates. Without Params/Steps coverage a ws `call` or a pipeline command
// would expose no argN in its MCP inputSchema and silently run with empty args.
func maxArgIndex(c *command.Command) int {
	max := -1
	for _, f := range []string{c.Path, c.Query, c.Body, c.Params} {
		max = scanArgIndices(f, max)
	}
	for _, step := range c.Steps {
		for _, f := range []string{step.Path, step.Query, step.Body} {
			max = scanArgIndices(f, max)
		}
	}
	return max
}

// buildSchema builds a minimal JSON Schema for a command's inputs.
// Properties: arg0…argN (string, optional), filter (string, optional),
// raw (boolean, optional). Required array is always empty.
func buildSchema(c *command.Command) json.RawMessage {
	props := make(map[string]any)

	// Positional arg properties.
	hi := maxArgIndex(c)
	for i := 0; i <= hi; i++ {
		key := fmt.Sprintf("arg%d", i)
		props[key] = map[string]any{
			"type":        "string",
			"description": fmt.Sprintf("positional argument %d", i),
		}
	}

	// Universal MCP-layer flags.
	props["filter"] = map[string]any{
		"type":        "string",
		"description": "jq filter over the response",
	}
	props["raw"] = map[string]any{
		"type":        "boolean",
		"description": "return raw response body",
	}

	schema := map[string]any{
		"type":       "object",
		"properties": props,
		"required":   []string{},
	}

	b, err := json.Marshal(schema)
	if err != nil {
		// Fallback to the bare minimum — should never happen.
		return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
	}
	return b
}

// toolName returns the MCP tool name for a service+command pair.
func toolName(svcName, cmdID string) string {
	return svcName + "_" + cmdID
}

// toolDesc builds the tool description, appending [WRITE] when the command
// mutates state.
func toolDesc(c *command.Command) string {
	if c.Write {
		return c.Help + " [WRITE]"
	}
	return c.Help
}

// toolTitle returns a short human-readable label for a tool, e.g.
// "Radarr: library list (movies)" or "radarr list" when no help is set.
func toolTitle(svcName, cmdID, help string) string {
	svc := titleCase(svcName)
	if clause := firstClause(help); clause != "" {
		return svc + ": " + clause
	}
	return svcName + " " + cmdID
}

// titleCase uppercases the first rune of s, leaving the rest untouched.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// firstClause returns the first sentence/clause of a help string: everything up
// to the first '.', ',', or newline, trimmed. Empty in → empty out.
func firstClause(help string) string {
	help = strings.TrimSpace(help)
	if i := strings.IndexAny(help, ".,\n"); i >= 0 {
		return strings.TrimSpace(help[:i])
	}
	return help
}

// boolPtr returns a pointer to b (the SDK's hint fields are *bool so "unset"
// is distinguishable from "false").
func boolPtr(b bool) *bool { return &b }

// buildAnnotations derives the MCP tool annotations from a command.
//
//   - Read command (c.Write == false): ReadOnlyHint=true. Per the spec the
//     destructive/idempotent hints are not meaningful when ReadOnlyHint is true,
//     so they are left unset.
//   - Write command (c.Write == true): ReadOnlyHint=false, with destructive and
//     idempotent hints inferred from the HTTP method where one exists:
//     DELETE/PUT are destructive + idempotent; POST/PATCH are additive + not
//     idempotent. A write with no HTTP method (a jsonrpc-ws `call`, a multi-step
//     pipeline) leaves DestructiveHint nil and IdempotentHint at the SDK default
//     — we don't guess for non-HTTP writes.
//
// Every tool sets OpenWorldHint=true: labctl tools call out to external/LAN
// services, so the domain of interaction is open, not a closed in-process world.
func buildAnnotations(svcName, cmdID string, c *command.Command) *mcp.ToolAnnotations {
	ann := &mcp.ToolAnnotations{
		Title:         toolTitle(svcName, cmdID, c.Help),
		OpenWorldHint: boolPtr(true),
	}
	if !c.Write {
		ann.ReadOnlyHint = true
		return ann
	}
	ann.ReadOnlyHint = false
	switch strings.ToUpper(c.Method) {
	case "DELETE", "PUT":
		// Full replacement / removal: destructive and idempotent.
		ann.DestructiveHint = boolPtr(true)
		ann.IdempotentHint = true
	case "POST", "PATCH":
		// Additive / partial: not destructive, not idempotent.
		ann.DestructiveHint = boolPtr(false)
		ann.IdempotentHint = false
	default:
		// Non-HTTP write (jsonrpc-ws call, pipeline): leave at SDK defaults.
	}
	return ann
}

// Options controls which tools BuildServer registers. The zero value reproduces
// the original behaviour (every non-ignored command of every service).
type Options struct {
	// ReadOnly omits every write command (c.Write == true) from the tool set.
	ReadOnly bool
	// Services, when non-empty, restricts the tool set to these service names;
	// every other service is omitted. Empty = all services.
	Services []string
}

// allowed reports whether a service name passes the Options.Services allowlist.
func (o Options) allowed(svcName string) bool {
	if len(o.Services) == 0 {
		return true
	}
	for _, s := range o.Services {
		if s == svcName {
			return true
		}
	}
	return false
}

// ValidateServices checks that every name in the allowlist names a loaded
// service, returning a clear error listing the unknown name(s) and the available
// services. An empty list is always valid (means "all services").
func ValidateServices(loaded *manifest.Loaded, names []string) error {
	if len(names) == 0 {
		return nil
	}
	var unknown []string
	for _, n := range names {
		if _, ok := loaded.Services[n]; !ok {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("unknown service(s): %s (available: %s)",
			strings.Join(unknown, ", "),
			strings.Join(loaded.SortedServiceNames(), ", "))
	}
	return nil
}

// BuildServer constructs an MCP server from the loaded manifests. It is
// exported so tests can drive the server without stdio. opts filters the tool
// set (read-only, service allowlist); the zero Options registers everything.
func BuildServer(
	loaded *manifest.Loaded,
	cfg manifest.Config,
	version string,
	tracer trace.Tracer,
	stderr io.Writer,
	opts Options,
) *mcp.Server {
	if version == "" {
		version = "dev"
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "labctl", Version: version}, nil)

	// registered tracks every tool name added so far, so a generic verb never
	// double-registers (it also lets verb registration defer to a same-named
	// manifest command).
	registered := make(map[string]bool)

	for _, svcName := range loaded.SortedServiceNames() {
		if !opts.allowed(svcName) {
			continue
		}
		svc := loaded.Services[svcName]
		cmds := command.FromManifest(svc)

		for _, id := range command.SortedIDs(cmds) {
			c := cmds[id]
			if c.MCPIgnore {
				continue
			}
			if opts.ReadOnly && c.Write {
				continue
			}

			// Capture loop variables for the closure.
			capturedSvc := svc
			capturedCmd := c
			capturedName := toolName(svcName, id)

			srv.AddTool(
				&mcp.Tool{
					Name:        capturedName,
					Description: toolDesc(c),
					InputSchema: buildSchema(c),
					Annotations: buildAnnotations(svcName, id, c),
				},
				func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return handleCall(ctx, req, capturedSvc, capturedCmd, cfg, tracer, stderr)
				},
			)
			registered[capturedName] = true
		}

		// Generic verbs: expose labctl's write capability (and the jsonrpc
		// `call`) as per-service MCP tools, mirroring the CLI's verb dispatch.
		registerVerbTools(srv, svc, cmds, opts, cfg, tracer, stderr, registered)
	}

	return srv
}

// verbToolOrder is the stable set of generic HTTP verbs exposed as MCP tools.
// HEAD is intentionally omitted — a body-less existence probe isn't a useful
// agent tool, and the CLI's HEAD verb has no MCP analogue here.
var verbToolOrder = []string{"get", "post", "put", "patch", "delete"}

// registerVerbTools adds the generic passthrough verbs for one service as MCP
// tools: <svc>_get/_post/_put/_patch/_delete for http transports, or <svc>_call
// for jsonrpc-ws. It mirrors the CLI's verb registration:
//
//   - A manifest command whose id equals the verb name wins (the generic tool is
//     skipped), matching the CLI's `if _, taken := cmds[verb]` guard.
//   - Write verbs (POST/PUT/PATCH/DELETE and the always-write `call`) are omitted
//     under opts.ReadOnly, reusing the exact same gate as named commands.
//   - registered guards against a duplicate AddTool of an already-claimed name.
func registerVerbTools(
	srv *mcp.Server,
	svc *manifest.Service,
	cmds map[string]*command.Command,
	opts Options,
	cfg manifest.Config,
	tracer trace.Tracer,
	stderr io.Writer,
	registered map[string]bool,
) {
	add := func(verb, method string, write bool) {
		// A named manifest command of the same id takes precedence.
		if _, taken := cmds[verb]; taken {
			return
		}
		if opts.ReadOnly && write {
			return
		}
		name := toolName(svc.Name, verb)
		if registered[name] {
			return
		}

		// A stub command drives annotations + the read/write gate only; the real
		// command is synthesized per call by command.Verb (which needs the path).
		stub := &command.Command{ID: verb, Method: method, Write: write}

		capturedSvc := svc
		capturedVerb := verb
		srv.AddTool(
			&mcp.Tool{
				Name:        name,
				Description: verbDesc(svc.Name, verb),
				InputSchema: verbSchema(verb),
				Annotations: buildAnnotations(svc.Name, verb, stub),
			},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return handleVerb(ctx, req, capturedSvc, capturedVerb, cfg, tracer, stderr)
			},
		)
		registered[name] = true
	}

	if svc.Transport == "jsonrpc-ws" {
		// jsonrpc write-ness depends on the runtime method, which isn't known at
		// registration time, so `call` is treated as a write (gated by ReadOnly).
		add("call", "", true)
		return
	}

	// http (or empty/default) transport: the five HTTP verbs.
	for _, verb := range verbToolOrder {
		method := command.HTTPVerbs[verb] // GET/POST/PUT/PATCH/DELETE
		add(verb, method, method != "GET")
	}
}

// handleCall dispatches one named-command tool call through engine.Execute. It
// extracts the arg0…argN positional template args from the request, then defers
// the shared engine-execute-and-render path to executeAndRender.
func handleCall(
	ctx context.Context,
	req *mcp.CallToolRequest,
	svc *manifest.Service,
	c *command.Command,
	cfg manifest.Config,
	tracer trace.Tracer,
	stderr io.Writer,
) (*mcp.CallToolResult, error) {
	raw, err := unmarshalArgs(req)
	if err != nil {
		return errorResult(fmt.Sprintf("unmarshal arguments: %v", err)), nil
	}

	// Extract positional args: arg0, arg1, …
	hi := maxArgIndex(c)
	args := make([]string, 0, hi+1)
	for i := 0; i <= hi; i++ {
		key := fmt.Sprintf("arg%d", i)
		if v, ok := raw[key]; ok {
			args = append(args, fmt.Sprintf("%v", v))
		} else {
			args = append(args, "")
		}
	}

	filter, _ := raw["filter"].(string)
	rawFlag, _ := raw["raw"].(bool)

	return executeAndRender(ctx, svc, c, args, filter, rawFlag, cfg, tracer, stderr), nil
}

// handleVerb dispatches one generic-verb tool call. It builds the args slice
// command.Verb expects from the structured tool inputs (path/query/body or
// method/params), synthesizes an ephemeral command, then runs the same
// engine-execute-and-render path as handleCall. Like the CLI's execVerb, the
// path/body/query/params are baked literally into the command, so the engine is
// driven with Args: nil (verb commands do not use {arg.N} templating).
func handleVerb(
	ctx context.Context,
	req *mcp.CallToolRequest,
	svc *manifest.Service,
	verb string,
	cfg manifest.Config,
	tracer trace.Tracer,
	stderr io.Writer,
) (*mcp.CallToolResult, error) {
	raw, err := unmarshalArgs(req)
	if err != nil {
		return errorResult(fmt.Sprintf("unmarshal arguments: %v", err)), nil
	}

	filter, _ := raw["filter"].(string)
	rawFlag, _ := raw["raw"].(bool)

	c, err := command.Verb(svc.Transport, verb, verbArgs(verb, raw))
	if err != nil {
		// e.g. missing path/method — surface as a tool-level error, never a panic.
		return errorResult(err.Error()), nil
	}

	return executeAndRender(ctx, svc, c, nil, filter, rawFlag, cfg, tracer, stderr), nil
}

// verbArgs maps the structured tool inputs of a generic-verb call to the
// positional args command.Verb consumes. An empty path/method yields an empty
// slice, which makes command.Verb return its usage error (surfaced as a
// tool-level error by handleVerb) rather than running against an empty path.
func verbArgs(verb string, raw map[string]any) []string {
	if verb == "call" {
		var args []string
		if method, _ := raw["method"].(string); method != "" {
			args = append(args, method)
			if params, _ := raw["params"].(string); params != "" {
				args = append(args, params)
			}
		}
		return args
	}

	var args []string
	if path, _ := raw["path"].(string); path != "" {
		args = append(args, path)
		switch verb {
		case "get":
			if q, _ := raw["query"].(string); q != "" {
				args = append(args, q)
			}
		case "post", "put", "patch":
			if b, _ := raw["body"].(string); b != "" {
				args = append(args, b)
			}
		}
	}
	return args
}

// unmarshalArgs decodes the raw tool-call arguments into a map. A nil/empty
// payload decodes to a nil map (every lookup misses, the SDK default).
func unmarshalArgs(req *mcp.CallToolRequest) (map[string]any, error) {
	var raw map[string]any
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &raw); err != nil {
			return nil, err
		}
	}
	return raw, nil
}

// executeAndRender runs one command through engine.Execute and renders the
// result, owning the per-call span. Shared by handleCall (named commands) and
// handleVerb (generic verbs) so both faces dispatch and render identically.
func executeAndRender(
	ctx context.Context,
	svc *manifest.Service,
	c *command.Command,
	args []string,
	filter string,
	rawFlag bool,
	cfg manifest.Config,
	tracer trace.Tracer,
	stderr io.Writer,
) *mcp.CallToolResult {
	ctx, span := tracer.Start(ctx, svc.Name+"_"+c.ID)
	defer span.End()

	res, err := engine.Execute(ctx, engine.Request{
		Config:  cfg,
		Service: svc,
		Command: c,
		Args:    args,
		Flags: engine.Flags{
			Filter: filter,
			Raw:    rawFlag,
		},
		Runner: nil, // real op resolver
	}, stderr)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return errorResult(err.Error())
	}

	if res.DryRunMsg != "" {
		span.SetStatus(codes.Ok, "")
		return textResult(res.DryRunMsg)
	}

	var sb strings.Builder
	if renderErr := output.Render(res.Body, res.Output, output.Options{
		Filter:        filter,
		Raw:           rawFlag,
		DefaultMode:   cfg.Defaults.Output,
		ResponseCodec: res.ResponseCodec,
	}, &sb); renderErr != nil {
		span.RecordError(renderErr)
		span.SetStatus(codes.Error, renderErr.Error())
		return errorResult(renderErr.Error())
	}

	span.SetStatus(codes.Ok, "")
	return textResult(sb.String())
}

// verbDesc builds a clear, self-documenting description for a generic-verb tool.
// Write verbs are flagged MUTATING in the prose (and keep the [WRITE] signal
// consistent with toolDesc).
func verbDesc(svcName, verb string) string {
	switch verb {
	case "get":
		return fmt.Sprintf("Generic GET against the %s API. path: API path (e.g. /api/v3/...); query: optional query string. Use filter for a jq expression over the JSON response.", svcName)
	case "post":
		return fmt.Sprintf("Generic POST against the %s API (MUTATING). path: API path; body: optional inline JSON. Use filter for a jq expression over the JSON response. [WRITE]", svcName)
	case "put":
		return fmt.Sprintf("Generic PUT (full replace) against the %s API (MUTATING). path: API path; body: optional inline JSON. Use filter for a jq expression over the JSON response. [WRITE]", svcName)
	case "patch":
		return fmt.Sprintf("Generic PATCH (partial update) against the %s API (MUTATING). path: API path; body: optional inline JSON. Use filter for a jq expression over the JSON response. [WRITE]", svcName)
	case "delete":
		return fmt.Sprintf("Generic DELETE against the %s API (MUTATING). path: API path. Use filter for a jq expression over the JSON response. [WRITE]", svcName)
	case "call":
		return fmt.Sprintf("Generic jsonrpc call against the %s API (MUTATING). method: jsonrpc method; params: optional JSON array. Use filter for a jq expression over the JSON response. [WRITE]", svcName)
	}
	return ""
}

// verbSchema builds the JSON Schema for a generic-verb tool. Unlike buildSchema
// (arg0…argN positional), verbs take named inputs — path/query/body or
// method/params — with path/method marked required. Every tool also carries the
// universal filter (string) and raw (boolean) flags.
func verbSchema(verb string) json.RawMessage {
	props := make(map[string]any)
	var required []string

	switch verb {
	case "get":
		props["path"] = map[string]any{"type": "string", "description": "API path, e.g. /api/v3/core/users/"}
		props["query"] = map[string]any{"type": "string", "description": "optional query string, e.g. search=foo&page=1"}
		required = []string{"path"}
	case "post", "put", "patch":
		props["path"] = map[string]any{"type": "string", "description": "API path, e.g. /api/v3/core/users/"}
		props["body"] = map[string]any{"type": "string", "description": "optional inline JSON request body"}
		required = []string{"path"}
	case "delete":
		props["path"] = map[string]any{"type": "string", "description": "API path, e.g. /api/v3/core/users/42/"}
		required = []string{"path"}
	case "call":
		props["method"] = map[string]any{"type": "string", "description": "jsonrpc method name, e.g. system.info"}
		props["params"] = map[string]any{"type": "string", "description": "optional JSON array of params, e.g. [\"arg\", 2]"}
		required = []string{"method"}
	}

	props["filter"] = map[string]any{"type": "string", "description": "jq filter over the response"}
	props["raw"] = map[string]any{"type": "boolean", "description": "return raw response body"}

	schema := map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}

	b, err := json.Marshal(schema)
	if err != nil {
		// Fallback to the bare minimum — should never happen.
		return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
	}
	return b
}

// textResult wraps text in a successful CallToolResult.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

// errorResult wraps a message in an error CallToolResult.
func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

// Serve builds the MCP server from the loaded manifests and runs it on stdio,
// blocking until ctx is cancelled or stdin closes.
func Serve(
	ctx context.Context,
	loaded *manifest.Loaded,
	cfg manifest.Config,
	version string,
	tracer trace.Tracer,
	stderr io.Writer,
	opts Options,
) error {
	srv := BuildServer(loaded, cfg, version, tracer, stderr, opts)
	return srv.Run(ctx, &mcp.StdioTransport{})
}
