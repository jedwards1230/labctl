package agentsafety

import "strings"

// AnnotationHints is the SDK-free result of the read-only/destructive/idempotent
// tool-safety policy. The MCP glue (mcpserver.buildAnnotations) copies these
// onto the SDK's *mcp.ToolAnnotations; Destructive is a *bool so "unset" (nil)
// stays distinguishable from an explicit false, matching the SDK hint field.
type AnnotationHints struct {
	ReadOnly    bool
	Destructive *bool
	Idempotent  bool
}

// Hints derives the tool-safety policy for a command from whether it writes and
// its HTTP method:
//
//   - read (write == false): ReadOnly=true. Per the spec the destructive and
//     idempotent hints are not meaningful when ReadOnly is true, so they are
//     left unset (Destructive nil, Idempotent false).
//   - write (write == true): ReadOnly=false, with destructive/idempotent inferred
//     from the HTTP method where one exists: DELETE/PUT are destructive +
//     idempotent; POST/PATCH are additive (not destructive) + not idempotent. A
//     write with no HTTP method (a jsonrpc-ws call, a multi-step pipeline) leaves
//     Destructive nil and Idempotent false — we don't guess for non-HTTP writes.
func Hints(write bool, method string) AnnotationHints {
	if !write {
		return AnnotationHints{ReadOnly: true}
	}
	h := AnnotationHints{ReadOnly: false}
	switch strings.ToUpper(method) {
	case "DELETE", "PUT":
		// Full replacement / removal: destructive and idempotent.
		d := true
		h.Destructive = &d
		h.Idempotent = true
	case "POST", "PATCH":
		// Additive / partial: not destructive, not idempotent.
		d := false
		h.Destructive = &d
		h.Idempotent = false
	default:
		// Non-HTTP write (jsonrpc-ws call, pipeline): leave at defaults.
	}
	return h
}
