package manifest

// Package manifest — OpenAPI inference (Phase 2).
//
// When a Service sets spec:, labctl fetches/reads the OpenAPI 3.x document and
// derives a Command for each operation. Explicit commands: entries override any
// inferred command with the same key.
//
// Supported: OpenAPI 3.0 and 3.1 (via libopenapi). Swagger 2.0 is NOT supported;
// a spec: pointing at a Swagger 2.0 document will fail validation with a clear error.
//
// SpecFilter semantics
// --------------------
// Matching is applied per-operation. An operation is included when ALL of:
//   - None of the Exclude patterns match it.
//   - Either Include is empty, OR at least one Include pattern matches it.
//
// Exclude wins over Include. Empty Include = "include all".
//
// Each pattern in Include/Exclude matches against any of these fields:
//   - operationId  — exact string (case-insensitive)
//   - HTTP method  — e.g. "GET", "POST" (case-insensitive)
//   - path         — shell glob, e.g. "/api/v1/*"
//   - tag           — any tag attached to the operation (case-insensitive, exact)
//
// Pattern syntax: raw strings matched against each field as described above.
// A pattern that contains a '*' or '?' is always treated as a path glob.
// Otherwise it is matched as a case-insensitive exact string against method,
// operationId, and tag; and also tried as a path glob (so "/foo" matches the path
// "/foo" exactly even without wildcards).

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pb33f/libopenapi"
)

// InferredCommands derives Command entries from the OpenAPI spec referenced by
// svc.Spec. If svc.Spec is empty it returns nil, nil. The configDir is used to
// resolve relative file paths.
//
// Exit-code contract: a bad spec URL/path or unparseable document is a config
// error → callers should surface it as exit 2. A valid URL that returns a
// non-200 or body that is not valid YAML/JSON is a decode error → exit 6.
func InferredCommands(svc *Service, configDir string) (map[string]Command, error) {
	if svc.Spec == "" {
		return nil, nil
	}

	raw, err := fetchSpec(svc.Spec, configDir)
	if err != nil {
		return nil, fmt.Errorf("spec %q: %w", svc.Spec, err)
	}

	ops, err := parseOperations(raw)
	if err != nil {
		return nil, fmt.Errorf("spec %q: %w", svc.Spec, err)
	}

	return buildCommands(ops, svc.SpecFilter), nil
}

// fetchSpec retrieves the raw spec bytes from a local file or HTTP(S) URL.
func fetchSpec(spec, configDir string) ([]byte, error) {
	if strings.HasPrefix(spec, "http://") || strings.HasPrefix(spec, "https://") {
		return fetchURL(spec)
	}
	// Treat as a file path. Resolve relative paths against the config dir.
	if !filepath.IsAbs(spec) && configDir != "" {
		spec = filepath.Join(configDir, spec)
	}
	b, err := os.ReadFile(spec)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	return b, nil
}

// fetchURL downloads a spec from an HTTP(S) URL with a 30-second timeout.
func fetchURL(u string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return b, nil
}

// specOp is a flat representation of one OpenAPI operation, used internally.
type specOp struct {
	OperationID string
	Method      string // uppercase, e.g. "GET"
	Path        string // e.g. "/api/v1/movies/{id}"
	Summary     string
	Description string
	Tags        []string
}

// parseOperations builds a slice of specOp from the raw spec bytes (OpenAPI 3.x only).
func parseOperations(raw []byte) ([]specOp, error) {
	doc, err := libopenapi.NewDocument(raw)
	if err != nil {
		return nil, fmt.Errorf("parse document: %w", err)
	}

	// Reject Swagger 2.0 — we only support OpenAPI 3.x.
	ver := doc.GetSpecInfo().SpecType
	if ver == "swagger" {
		return nil, fmt.Errorf("swagger 2.0 is not supported; spec: requires OpenAPI 3.x")
	}

	model, err := doc.BuildV3Model()
	if err != nil {
		return nil, fmt.Errorf("build model: %w", err)
	}
	if model == nil || model.Model.Paths == nil || model.Model.Paths.PathItems == nil {
		return nil, nil // empty spec is valid
	}

	var ops []specOp
	for pair := model.Model.Paths.PathItems.First(); pair != nil; pair = pair.Next() {
		path := pair.Key()
		item := pair.Value()
		if item == nil {
			continue
		}
		pathOps := item.GetOperations()
		if pathOps == nil {
			continue
		}
		for opPair := pathOps.First(); opPair != nil; opPair = opPair.Next() {
			method := strings.ToUpper(opPair.Key())
			op := opPair.Value()
			if op == nil {
				continue
			}
			so := specOp{
				OperationID: op.OperationId,
				Method:      method,
				Path:        path,
				Summary:     op.Summary,
				Description: op.Description,
				Tags:        op.Tags,
			}
			ops = append(ops, so)
		}
	}
	return ops, nil
}

// buildCommands converts specOps into Command entries, applying SpecFilter.
func buildCommands(ops []specOp, filter SpecFilter) map[string]Command {
	out := make(map[string]Command, len(ops))
	for _, op := range ops {
		if !filterIncludes(op, filter) {
			continue
		}
		key := commandKey(op)
		help := op.Summary
		if help == "" {
			help = op.Description
		}
		// Truncate long descriptions to first sentence for the help field.
		if len(help) > 120 {
			if i := strings.IndexAny(help, ".\n"); i > 0 {
				help = help[:i+1]
			} else {
				help = help[:120] + "…"
			}
		}
		out[key] = Command{
			Help:   help,
			Method: op.Method,
			Path:   op.Path,
		}
	}
	return out
}

// commandKey returns the stable key used to identify an inferred command.
// Preference: slugified operationId → method+path slug.
func commandKey(op specOp) string {
	if op.OperationID != "" {
		return slugify(op.OperationID)
	}
	// Fallback: method + path slug, e.g. "get-api-v1-movies"
	combined := strings.ToLower(op.Method) + "-" + op.Path
	return slugify(combined)
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugify converts an arbitrary string into a lowercase hyphen-separated slug.
func slugify(s string) string {
	s = strings.ToLower(s)
	s = nonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// filterIncludes reports whether the operation passes the SpecFilter rules:
//
//  1. If any Exclude pattern matches → false (Exclude wins).
//  2. If Include is empty → true (no restriction).
//  3. If any Include pattern matches → true.
//  4. Otherwise → false.
func filterIncludes(op specOp, f SpecFilter) bool {
	for _, pat := range f.Exclude {
		if matchesOp(pat, op) {
			return false
		}
	}
	if len(f.Include) == 0 {
		return true
	}
	for _, pat := range f.Include {
		if matchesOp(pat, op) {
			return true
		}
	}
	return false
}

// matchesOp tests whether a single filter pattern matches the operation.
// A pattern is matched against: path (glob), HTTP method (exact), operationId
// (exact), and each tag (exact). All comparisons are case-insensitive.
//
// Path glob notes: "*" matches any sequence of characters including "/" (unlike
// filepath.Match which stops at path separators). "?" matches exactly one
// character, also including "/". This lets "/pets*" match "/pets/{petId}".
func matchesOp(pattern string, op specOp) bool {
	// Path glob match (case-insensitive; * crosses path separators).
	if pathGlobMatch(strings.ToLower(pattern), strings.ToLower(op.Path)) {
		return true
	}
	// Method exact match.
	if strings.EqualFold(pattern, op.Method) {
		return true
	}
	// OperationId exact match.
	if op.OperationID != "" && strings.EqualFold(pattern, op.OperationID) {
		return true
	}
	// Tag exact match.
	for _, tag := range op.Tags {
		if strings.EqualFold(pattern, tag) {
			return true
		}
	}
	return false
}

// pathGlobMatch reports whether pattern matches s. Unlike filepath.Match, "*"
// matches any sequence of characters including "/" so that "/pets*" matches
// "/pets/{petId}". "?" matches exactly one character (including "/"). No
// character-class syntax is supported.
func pathGlobMatch(pattern, s string) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			// Skip consecutive stars.
			for len(pattern) > 0 && pattern[0] == '*' {
				pattern = pattern[1:]
			}
			if len(pattern) == 0 {
				return true // trailing * matches everything
			}
			// Try matching the rest of pattern against every suffix of s.
			for i := 0; i <= len(s); i++ {
				if pathGlobMatch(pattern, s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			pattern = pattern[1:]
			s = s[1:]
		default:
			if len(s) == 0 || pattern[0] != s[0] {
				return false
			}
			pattern = pattern[1:]
			s = s[1:]
		}
	}
	return len(s) == 0
}
