// Package views serves the single universal MCP Apps result View — a small
// built single-file HTML/JS bundle that renders any read-tool's
// StructuredContent (table/record/tree, shape-adaptive) inside an MCP Apps
// host. The Go side never knows HTML/CSS/JS — it just embeds and serves the
// one file, mirroring catalog/catalog.go's //go:embed pattern: the package is
// dependency-free so it can be imported by mcpserver without pulling anything
// else in.
//
// The HTML is built from the separate views/ TS/Vite project at the repo
// root (see views/README.md) and committed here so `go build` never needs
// npm. LABCTL_VIEWS_DIR overrides the embedded copy with a live file from
// disk — mirrors LABCTL_CONFIG_DIR — for iterating on views/ without a Go
// rebuild.
package views

import (
	_ "embed"
	"os"
	"path/filepath"
)

// ResultMIMEType is the MIME type advertised for the ui://labctl/result
// resource: the ext-apps SDK's RESOURCE_MIME_TYPE constant
// (@modelcontextprotocol/ext-apps/server, see views/README.md), confirmed
// against the cloned SDK source. An MCP Apps host gates UI rendering on this
// exact value.
const ResultMIMEType = "text/html;profile=mcp-app"

//go:embed result.html
var embeddedResultHTML []byte

// ResultHTML returns the built single-file result-View HTML: the contents of
// LABCTL_VIEWS_DIR/result.html when that env var is set and the file is
// readable, otherwise the embedded copy. Read once per call so a server
// rebuilt with LABCTL_VIEWS_DIR set always picks up the latest build on disk
// without a Go rebuild; BuildServer calls this once at server-construction
// time, matching "read at server-build time" in the dev-loop contract.
func ResultHTML() []byte {
	if dir := os.Getenv("LABCTL_VIEWS_DIR"); dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, "result.html")); err == nil {
			return b
		}
	}
	return embeddedResultHTML
}
