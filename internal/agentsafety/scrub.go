// Package agentsafety consolidates labctl's agent-safety machinery — secret
// scrubbing, dry-run preview rendering, the exit-code taxonomy + classifier,
// the tool-annotation policy, and mutation audit logging — into one SDK-light
// package shared by the engine, the CLI, and the MCP server.
//
// It is deliberately unopinionated: it renders, classifies, and records, but it
// gates nothing. Write-confirmation, elicitation, and read-only enforcement are
// NOT part of this package (see CLAUDE.md's "unopinionated executor" principle).
//
// Import layering: agentsafety may import transport/secret/manifest/command and
// the MCP SDK, but none of engine/cli/mcpserver — those import agentsafety.
package agentsafety

import (
	"sort"
	"strings"
)

// redactedPlaceholder is the token a Scrubber substitutes for a secret value.
const redactedPlaceholder = "<redacted>"

// Scrubber replaces known secret values with <redacted> anywhere they appear in
// a diagnostic string. It is built from a snapshot of resolved secret values and
// threaded into the transport layer so verbose output, error strings, and (via
// those) span errors never echo a live credential — even when the secret lands
// in a URL query param (e.g. ?apikey={secret.X}), not just a redactable header.
//
// Field-position-agnostic by design: it matches on the value, so it does not
// matter which part of the request carried the secret.
type Scrubber struct {
	values []string // deduped, non-empty, sorted longest-first
}

// NewScrubber builds a Scrubber from secret values. It dedupes, drops empties,
// and sorts longest-first so a short secret that is a substring of a longer one
// does not pre-empt the longer match. A scrubber built from no usable values is
// the identity transform.
func NewScrubber(values []string) *Scrubber {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" {
			continue // never blanket-replace on an empty value
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j]) // longest-first
		}
		return out[i] < out[j] // stable tiebreak
	})
	return &Scrubber{values: out}
}

// Scrub replaces every occurrence of a known secret value with <redacted>. A
// nil/empty scrubber (or empty text) returns the input unchanged. Never panics.
func (s *Scrubber) Scrub(text string) string {
	if s == nil || len(s.values) == 0 || text == "" {
		return text
	}
	for _, v := range s.values {
		text = strings.ReplaceAll(text, v, redactedPlaceholder)
	}
	return text
}
