// Package command holds the format-neutral Command model that every producer
// emits — the hand-written commands: block today, OpenAPI inference in Phase 2 —
// and the executor consumes. Keeping one model means CLI and MCP behave
// identically regardless of where a command came from.
package command

import (
	"sort"
	"strings"

	"github.com/jedwards1230/labctl/internal/manifest"
)

// Command is one invokable operation against a service.
type Command struct {
	ID         string
	Help       string
	Endpoint   string // "" = default endpoint
	Method     string // HTTP verb OR jsonrpc method name
	Path       string // template; may embed {arg.N}/{secret.X}/{env.Y}/{var}
	Query      string // template
	Headers    map[string]string
	Body       string // inline template or @file
	Params     string // jsonrpc params (templated JSON array)
	NoAuth     bool
	Codec      manifest.Codec
	Pagination manifest.Pagination
	Output     manifest.Output
	MCPIgnore  bool
	Steps      []manifest.Step
	Write      bool // non-GET / mutating jsonrpc — informational only (binary gates nothing)
}

// FromManifest builds the command set declared in a service's commands: block.
func FromManifest(svc *manifest.Service) map[string]*Command {
	out := make(map[string]*Command, len(svc.Commands))
	for id, c := range svc.Commands {
		out[id] = &Command{
			ID:         id,
			Help:       c.Help,
			Endpoint:   c.Endpoint,
			Method:     c.Method,
			Path:       c.Path,
			Query:      c.Query,
			Headers:    c.Headers,
			Body:       c.Body,
			Params:     c.Params,
			NoAuth:     c.NoAuth,
			Codec:      c.Codec,
			Pagination: orDefault(c.Pagination, svc.Pagination),
			Output:     resolveOutput(c.Output, svc.Output),
			MCPIgnore:  c.MCPIgnore,
			Steps:      c.Steps,
			Write:      isWrite(svc.Transport, c.Method),
		}
	}
	return out
}

// SortedIDs returns command ids in stable order (for --help).
func SortedIDs(cmds map[string]*Command) []string {
	ids := make([]string, 0, len(cmds))
	for id := range cmds {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func isWrite(transport, method string) bool {
	if transport == "jsonrpc-ws" {
		// jsonrpc methods aren't verbs; treat read-ish query/ping/info as reads.
		m := strings.ToLower(method)
		for _, r := range []string{".query", ".get", ".config", "core.ping", ".list", ".info"} {
			if strings.Contains(m, r) {
				return false
			}
		}
		return true
	}
	switch strings.ToUpper(method) {
	case "", "GET", "HEAD", "OPTIONS":
		return false
	default:
		return true
	}
}

// orDefault returns the command pagination if it sets a style, else the service default.
func orDefault(cmd, svc manifest.Pagination) manifest.Pagination {
	if cmd.Style != "" {
		return cmd
	}
	return svc
}

// resolveOutput merges a command's output over the service default. A command's
// `filter` (or `default_filter`) wins; mode falls back to the service.
func resolveOutput(cmd, svc manifest.Output) manifest.Output {
	out := manifest.Output{
		DefaultFilter: firstNonEmpty(cmd.Filter, cmd.DefaultFilter, svc.DefaultFilter, svc.Filter),
		Mode:          firstNonEmpty(cmd.Mode, svc.Mode),
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
