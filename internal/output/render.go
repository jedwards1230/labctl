// Package output applies a jq filter (gojq, pure-Go) to a response body and
// renders the result in one of the supported modes: json (pretty), raw
// (passthrough, no filter), or scalar (bare string). table is deferred past v1.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/itchyny/gojq"
	"github.com/jedwards1230/labctl/internal/manifest"
)

// Options controls a single render.
type Options struct {
	Filter string // explicit --filter; overrides the command default
	Raw    bool   // --raw; bypass jq entirely
	Mode   string // json|raw|scalar; overrides the command/service default
}

// Render filters body and writes the result to w. defaultFilter/defaultMode come
// from the command's resolved Output; opts (CLI flags) override them.
func Render(body []byte, out manifest.Output, opts Options, w io.Writer) error {
	mode := firstNonEmpty(opts.Mode, out.Mode, "json")
	if opts.Raw || mode == "raw" {
		_, err := w.Write(ensureTrailingNewline(body))
		return err
	}

	filter := firstNonEmpty(opts.Filter, out.DefaultFilter, ".")

	var input any
	if err := json.Unmarshal(body, &input); err != nil {
		// Not JSON — in scalar mode emit as-is; otherwise it's an error.
		if mode == "scalar" {
			_, werr := io.WriteString(w, strings.TrimRight(string(body), "\n")+"\n")
			return werr
		}
		return fmt.Errorf("decode response as JSON: %w", err)
	}

	query, err := gojq.Parse(filter)
	if err != nil {
		return fmt.Errorf("parse filter %q: %w", filter, err)
	}

	iter := query.Run(input)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := v.(error); ok {
			return fmt.Errorf("filter: %w", err)
		}
		if err := renderValue(v, mode, w); err != nil {
			return err
		}
	}
	return nil
}

func renderValue(v any, mode string, w io.Writer) error {
	switch mode {
	case "scalar":
		return renderScalar(v, w)
	default: // json
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
}

func renderScalar(v any, w io.Writer) error {
	switch t := v.(type) {
	case nil:
		_, err := io.WriteString(w, "\n")
		return err
	case string:
		_, err := io.WriteString(w, t+"\n")
		return err
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = io.WriteString(w, string(b)+"\n")
		return err
	}
}

func ensureTrailingNewline(b []byte) []byte {
	if len(b) == 0 || b[len(b)-1] == '\n' {
		return b
	}
	return append(b, '\n')
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
