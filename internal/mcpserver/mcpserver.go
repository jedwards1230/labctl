// Package mcpserver exposes loaded manifests as MCP tools over stdio. Each
// non-ignored command in each service becomes one tool named
// <service>_<command>. All tool calls dispatch through engine.Execute — the
// same path as the CLI — so behaviour is identical from both faces.
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
		}
	}

	return srv
}

// handleCall dispatches one tool call through engine.Execute.
func handleCall(
	ctx context.Context,
	req *mcp.CallToolRequest,
	svc *manifest.Service,
	c *command.Command,
	cfg manifest.Config,
	tracer trace.Tracer,
	stderr io.Writer,
) (*mcp.CallToolResult, error) {
	spanName := svc.Name + "_" + c.ID
	ctx, span := tracer.Start(ctx, spanName)
	defer span.End()

	// Unmarshal raw arguments.
	var raw map[string]any
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &raw); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return errorResult(fmt.Sprintf("unmarshal arguments: %v", err)), nil
		}
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

	// Extract filter and raw flag.
	filter, _ := raw["filter"].(string)
	rawFlag, _ := raw["raw"].(bool)

	engReq := engine.Request{
		Config:  cfg,
		Service: svc,
		Command: c,
		Args:    args,
		Flags: engine.Flags{
			Filter: filter,
			Raw:    rawFlag,
		},
		Runner: nil, // real op resolver
	}

	res, err := engine.Execute(ctx, engReq, stderr)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return errorResult(err.Error()), nil
	}

	if res.DryRunMsg != "" {
		span.SetStatus(codes.Ok, "")
		return textResult(res.DryRunMsg), nil
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
		return errorResult(renderErr.Error()), nil
	}

	span.SetStatus(codes.Ok, "")
	return textResult(sb.String()), nil
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
