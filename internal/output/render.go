// Package output applies a jq filter (gojq, pure-Go) to a response body and
// renders the result in one of the supported modes: json (pretty), raw
// (passthrough, no filter), or scalar (bare string). table is deferred past v1.
package output

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/itchyny/gojq"
	"github.com/jedwards1230/labctl/internal/manifest"
)

// Options controls a single render.
type Options struct {
	Filter        string // explicit --filter; overrides the command default
	Raw           bool   // --raw; bypass jq entirely
	Mode          string // json|raw|scalar; overrides the command/service default
	ResponseCodec string // "xml", "json", or "" (empty = json default)
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

	// Decode the response body. XML responses are decoded to a map[string]any
	// tree first so the gojq filter can consume them identically to JSON.
	var input any
	var decodeErr error
	if opts.ResponseCodec == "xml" {
		input, decodeErr = DecodeXML(body)
	} else {
		decodeErr = json.Unmarshal(body, &input)
	}
	if decodeErr != nil {
		// Not decodable — in scalar mode emit as-is; otherwise it's an error.
		if mode == "scalar" {
			_, werr := io.WriteString(w, strings.TrimRight(string(body), "\n")+"\n")
			return werr
		}
		codec := opts.ResponseCodec
		if codec == "" {
			codec = "JSON"
		}
		return fmt.Errorf("decode response as %s: %w", strings.ToUpper(codec), decodeErr)
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

// DecodeXML parses XML into a map[string]any tree that gojq filters can
// consume. The convention is:
//
//   - Each XML element becomes a key in the parent map. Its value is either a
//     string (leaf with text content only), a map[string]any (element with
//     children or attributes), or []any (when multiple sibling elements share
//     the same tag name).
//   - Attributes are surfaced under the "@attrs" key as a map[string]string
//     within their element's map.
//   - The root element's tag name is the top-level key, so a Sunshine
//     /serverinfo response like <root status_code="200"><hostname>x</hostname>
//     …</root> is accessible as .root.hostname or .root["@attrs"].status_code.
//
// This convention is intentionally simple: one level of element → map
// substitution, no namespace handling, text content trimmed of whitespace.
func DecodeXML(data []byte) (any, error) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	// Skip over the XML declaration / processing instructions.
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("xml: %w", err)
		}
		if _, ok := tok.(xml.StartElement); ok {
			// Put the start element back by re-creating a decoder from the full input.
			break
		}
	}

	// Re-decode from scratch for simplicity — the above loop just confirmed
	// there is at least one start element.
	dec = xml.NewDecoder(strings.NewReader(string(data)))
	node, err := xmlNodeToMap(dec)
	if err != nil {
		return nil, err
	}
	return node, nil
}

// xmlNodeToMap finds the first root element and returns a map[string]any where
// the single key is the root element's tag name and the value is the decoded
// content. For example <root><hostname>x</hostname></root> returns
// {"root": {"hostname": "x"}}.
func xmlNodeToMap(dec *xml.Decoder) (map[string]any, error) {
	// Skip non-start tokens (XML declaration, whitespace, comments).
	var start xml.StartElement
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("xml read: %w", err)
		}
		var ok bool
		if start, ok = tok.(xml.StartElement); ok {
			break
		}
	}
	inner, err := xmlElementToMap(dec, start)
	if err != nil {
		return nil, err
	}
	// Wrap under the root tag name so callers reach content as .root.hostname.
	return map[string]any{start.Name.Local: inner}, nil
}

// xmlElementToMap decodes one element (whose StartElement has already been
// consumed) and all its children into a map[string]any.
func xmlElementToMap(dec *xml.Decoder, start xml.StartElement) (map[string]any, error) {
	elem := make(map[string]any)

	// Surface XML attributes under "@attrs" as map[string]any so gojq can
	// iterate and filter them without a type panic.
	if len(start.Attr) > 0 {
		attrs := make(map[string]any, len(start.Attr))
		for _, a := range start.Attr {
			attrs[a.Name.Local] = a.Value
		}
		elem["@attrs"] = attrs
	}

	var textBuf strings.Builder

	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("xml read inside <%s>: %w", start.Name.Local, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			child, err := xmlElementToMap(dec, t)
			if err != nil {
				return nil, err
			}
			key := t.Name.Local
			// Determine the child's value: if it has only a single "text" key and
			// no "@attrs", unwrap to a plain string for friendlier access.
			var childVal any = child
			if len(child) == 1 {
				if txt, ok := child["text"]; ok {
					childVal = txt
				}
			}
			if existing, ok := elem[key]; ok {
				// Multiple siblings with the same tag → accumulate into []any.
				switch ev := existing.(type) {
				case []any:
					elem[key] = append(ev, childVal)
				default:
					elem[key] = []any{ev, childVal}
				}
			} else {
				elem[key] = childVal
			}
		case xml.EndElement:
			txt := strings.TrimSpace(textBuf.String())
			if txt != "" {
				elem["text"] = txt
			}
			return elem, nil
		case xml.CharData:
			textBuf.Write(t)
		}
	}
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
