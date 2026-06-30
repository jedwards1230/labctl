package output

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/jedwards1230/labctl/internal/manifest"
)

func render(t *testing.T, body string, out manifest.Output, opts Options) string {
	t.Helper()
	var b strings.Builder
	if err := Render([]byte(body), out, opts, &b); err != nil {
		t.Fatalf("Render error: %v", err)
	}
	return b.String()
}

func filtered(t *testing.T, body string, out manifest.Output, opts Options) any {
	t.Helper()
	v, err := Filtered([]byte(body), out, opts)
	if err != nil {
		t.Fatalf("Filtered error: %v", err)
	}
	return v
}

func TestRenderDefaultFilter(t *testing.T) {
	got := render(t, `{"a":1,"b":2}`, manifest.Output{DefaultFilter: ".a"}, Options{})
	if strings.TrimSpace(got) != "1" {
		t.Fatalf("got %q", got)
	}
}

// TestRenderJSONNoHTMLEscape proves json mode emits <, >, & literally (matching
// jq) rather than the default encoder's < / & escapes.
func TestRenderJSONNoHTMLEscape(t *testing.T) {
	got := render(t, `{"s":"a?b&c<d>"}`, manifest.Output{DefaultFilter: ".s"}, Options{})
	// The literal substring only appears verbatim when HTML escaping is OFF; with
	// escaping ON the encoder would emit a?b&c<d> instead.
	if !strings.Contains(got, "a?b&c<d>") {
		t.Fatalf("json render = %q, want literal a?b&c<d> (no HTML escaping)", got)
	}
}

// TestRenderScalarNoHTMLEscape proves scalar mode also emits non-scalar JSON
// without HTML escaping.
func TestRenderScalarNoHTMLEscape(t *testing.T) {
	got := render(t, `{"v":["x&y","p<q"]}`, manifest.Output{DefaultFilter: ".v", Mode: "scalar"}, Options{})
	if !strings.Contains(got, "x&y") || !strings.Contains(got, "p<q") {
		t.Fatalf("scalar render = %q, want literal x&y and p<q (no HTML escaping)", got)
	}
}

func TestRenderFilterOverride(t *testing.T) {
	got := render(t, `{"a":1,"b":2}`, manifest.Output{DefaultFilter: ".a"}, Options{Filter: ".b"})
	if strings.TrimSpace(got) != "2" {
		t.Fatalf("got %q", got)
	}
}

// TestRenderModePrecedence proves output-mode resolution: flag > command/service
// out.Mode > defaults.output (Options.DefaultMode) > "json".
func TestRenderModePrecedence(t *testing.T) {
	body := `{"s":"hello"}`
	out := manifest.Output{DefaultFilter: ".s"}

	// defaults.output=scalar, no command mode, no flag → scalar (bare string).
	got := render(t, body, out, Options{DefaultMode: "scalar"})
	if strings.TrimSpace(got) != "hello" {
		t.Fatalf("defaults.output=scalar → %q, want bare 'hello'", got)
	}

	// command mode beats defaults.output.
	got = render(t, body, manifest.Output{DefaultFilter: ".s", Mode: "json"}, Options{DefaultMode: "scalar"})
	if !strings.Contains(got, `"hello"`) {
		t.Fatalf("command mode=json should win over defaults.output=scalar; got %q", got)
	}

	// flag (Options.Mode) beats everything.
	got = render(t, body, manifest.Output{DefaultFilter: ".s", Mode: "json"}, Options{Mode: "scalar", DefaultMode: "json"})
	if strings.TrimSpace(got) != "hello" {
		t.Fatalf("flag mode=scalar should win; got %q", got)
	}

	// nothing set → json ultimate fallback.
	got = render(t, body, out, Options{})
	if !strings.Contains(got, `"hello"`) {
		t.Fatalf("no mode anywhere → json fallback; got %q", got)
	}
}

func TestRenderRaw(t *testing.T) {
	body := `{"a":  1}`
	got := render(t, body, manifest.Output{DefaultFilter: ".a"}, Options{Raw: true})
	if strings.TrimSpace(got) != body {
		t.Fatalf("raw should bypass jq; got %q", got)
	}
}

func TestRenderScalar(t *testing.T) {
	got := render(t, `{"version":"6.0.4"}`, manifest.Output{DefaultFilter: ".version", Mode: "scalar"}, Options{})
	if got != "6.0.4\n" {
		t.Fatalf("scalar should be bare string; got %q", got)
	}
}

func TestRenderMapProjection(t *testing.T) {
	got := render(t, `[{"id":1,"x":9},{"id":2,"x":8}]`, manifest.Output{DefaultFilter: "map(.id)"}, Options{})
	want := "[\n  1,\n  2\n]\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRenderBadFilter(t *testing.T) {
	var b strings.Builder
	err := Render([]byte(`{}`), manifest.Output{DefaultFilter: ".["}, Options{}, &b)
	if err == nil {
		t.Fatal("expected parse error on bad filter")
	}
}

// TestRenderXMLSunshineServerinfo verifies that a Sunshine /serverinfo XML
// response is decoded to a map tree where .root.hostname extracts the hostname.
// The XML convention is: root element tag → top-level key; child elements →
// nested map keys; attributes surfaced under "@attrs".
func TestRenderXMLSunshineServerinfo(t *testing.T) {
	// Sample trimmed from the real Sunshine /serverinfo endpoint.
	xmlBody := `<?xml version="1.0" encoding="utf-8"?>
<root status_code="200">
  <hostname>desktop-1</hostname>
  <appversion>7.1.431.-1</appversion>
  <platform>windows</platform>
</root>`

	// Decode via the XML path and extract .root.hostname via jq.
	got := render(t, xmlBody, manifest.Output{DefaultFilter: ".root.hostname"}, Options{
		ResponseCodec: "xml",
		Mode:          "scalar",
	})
	if got != "desktop-1\n" {
		t.Fatalf("xml .root.hostname = %q, want %q", got, "desktop-1\n")
	}

	// Also verify appversion is reachable.
	got2 := render(t, xmlBody, manifest.Output{DefaultFilter: ".root.appversion"}, Options{
		ResponseCodec: "xml",
		Mode:          "scalar",
	})
	if got2 != "7.1.431.-1\n" {
		t.Fatalf("xml .root.appversion = %q, want %q", got2, "7.1.431.-1\n")
	}
}

// TestRenderXMLAttributes verifies that XML attributes are surfaced under "@attrs".
func TestRenderXMLAttributes(t *testing.T) {
	xmlBody := `<root status_code="200"><hostname>myhost</hostname></root>`

	got := render(t, xmlBody, manifest.Output{DefaultFilter: `.root["@attrs"].status_code`}, Options{
		ResponseCodec: "xml",
		Mode:          "scalar",
	})
	if got != "200\n" {
		t.Fatalf("xml @attrs.status_code = %q, want %q", got, "200\n")
	}
}

// TestRenderXMLRepeatedChildren verifies that repeated sibling elements are
// accumulated into a []any array.
func TestRenderXMLRepeatedChildren(t *testing.T) {
	xmlBody := `<items><item>a</item><item>b</item><item>c</item></items>`

	got := render(t, xmlBody, manifest.Output{DefaultFilter: ".items.item | length"}, Options{
		ResponseCodec: "xml",
		Mode:          "scalar",
	})
	if strings.TrimSpace(got) != "3" {
		t.Fatalf("repeated children length = %q, want 3", got)
	}
}

// TestFilteredMatchesRenderSingleResult is the explicit "Filtered matches the
// text path" guarantee the MCP server's StructuredContent.result relies on:
// for every filter that yields exactly one jq result, Filtered's value
// marshals to the same JSON as Render's text output, for the SAME body/out/
// opts inputs used by the Render tests above.
func TestFilteredMatchesRenderSingleResult(t *testing.T) {
	cases := []struct {
		name string
		body string
		out  manifest.Output
		opts Options
	}{
		{"default filter", `{"a":1,"b":2}`, manifest.Output{DefaultFilter: ".a"}, Options{}},
		{"filter override", `{"a":1,"b":2}`, manifest.Output{DefaultFilter: ".a"}, Options{Filter: ".b"}},
		{"map projection", `[{"id":1,"x":9},{"id":2,"x":8}]`, manifest.Output{DefaultFilter: "map(.id)"}, Options{}},
		{"scalar mode string", `{"version":"6.0.4"}`, manifest.Output{DefaultFilter: ".version", Mode: "scalar"}, Options{}},
		{"json mode whole object", `{"s":"hello"}`, manifest.Output{DefaultFilter: ".s"}, Options{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotVal := filtered(t, tc.body, tc.out, tc.opts)
			gotJSON, err := json.Marshal(gotVal)
			if err != nil {
				t.Fatalf("marshal Filtered value: %v", err)
			}

			wantText := strings.TrimRight(render(t, tc.body, tc.out, tc.opts), "\n")
			var wantVal any
			if jsonErr := json.Unmarshal([]byte(wantText), &wantVal); jsonErr != nil {
				// scalar mode on a bare string writes it unquoted — not valid JSON
				// on its own, so treat the literal text as the expected string value.
				wantVal = wantText
			}
			wantJSON, err := json.Marshal(wantVal)
			if err != nil {
				t.Fatalf("marshal Render-derived value: %v", err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("Filtered = %s, want (derived from Render text) %s", gotJSON, wantJSON)
			}
		})
	}
}

// TestFilteredRaw proves --raw returns the decoded JSON body (no jq, no
// double-encoding) as the structured Go value, matching Render's raw
// passthrough semantics but as a value instead of bytes.
func TestFilteredRaw(t *testing.T) {
	got := filtered(t, `{"a":  1}`, manifest.Output{DefaultFilter: ".a"}, Options{Raw: true})
	want := map[string]any{"a": float64(1)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// TestFilteredRawNonJSONFallsBackToString proves a non-JSON raw body passes
// through as the trimmed raw string rather than erroring — no double-encoding,
// matching Render's raw passthrough of arbitrary bytes.
func TestFilteredRawNonJSONFallsBackToString(t *testing.T) {
	got := filtered(t, "not json\n", manifest.Output{}, Options{Raw: true})
	if got != "not json" {
		t.Fatalf("got %#v, want literal string %q", got, "not json")
	}
}

// TestFilteredModeRawViaOutputMode proves out.Mode=="raw" (not just the --raw
// flag) also takes the raw path, mirroring Render's mode resolution.
func TestFilteredModeRawViaOutputMode(t *testing.T) {
	got := filtered(t, `{"a":1}`, manifest.Output{Mode: "raw"}, Options{})
	want := map[string]any{"a": float64(1)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// TestFilteredMultipleResultsCollectIntoSlice proves a filter yielding more
// than one jq result (e.g. `.[]`) collects into a Go slice — Render instead
// writes each result as a separate JSON value to the text stream, but
// Filtered must return ONE value, per the StructuredContent.result contract.
func TestFilteredMultipleResultsCollectIntoSlice(t *testing.T) {
	got := filtered(t, `[1,2,3]`, manifest.Output{DefaultFilter: ".[]"}, Options{})
	want := []any{float64(1), float64(2), float64(3)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// TestFilteredZeroResultsIsNil proves a filter yielding zero results (e.g. an
// empty() pipeline) returns a nil value rather than an empty slice.
func TestFilteredZeroResultsIsNil(t *testing.T) {
	got := filtered(t, `{}`, manifest.Output{DefaultFilter: "empty"}, Options{})
	if got != nil {
		t.Fatalf("got %#v, want nil", got)
	}
}

// TestFilteredBadFilter proves an invalid jq filter is a parse error, just
// like Render.
func TestFilteredBadFilter(t *testing.T) {
	_, err := Filtered([]byte(`{}`), manifest.Output{DefaultFilter: ".["}, Options{})
	if err == nil {
		t.Fatal("expected parse error on bad filter")
	}
}

// TestFilteredUndecodableBodyScalarFallback proves an undecodable body in
// scalar mode falls back to the trimmed raw string, matching Render's scalar
// fallback (rather than erroring).
func TestFilteredUndecodableBodyScalarFallback(t *testing.T) {
	got := filtered(t, "plain text\n", manifest.Output{DefaultFilter: "."}, Options{Mode: "scalar"})
	if got != "plain text" {
		t.Fatalf("got %#v, want %q", got, "plain text")
	}
}

// TestFilteredUndecodableBodyNonScalarErrors proves an undecodable body in a
// non-scalar mode is an error, matching Render.
func TestFilteredUndecodableBodyNonScalarErrors(t *testing.T) {
	_, err := Filtered([]byte("plain text"), manifest.Output{DefaultFilter: "."}, Options{})
	if err == nil {
		t.Fatal("expected decode error for undecodable body in non-scalar mode")
	}
}

// TestFilteredXML proves the XML decode path (DecodeXML) feeds Filtered the
// same way it feeds Render.
func TestFilteredXML(t *testing.T) {
	xmlBody := `<root status_code="200"><hostname>myhost</hostname></root>`
	got := filtered(t, xmlBody, manifest.Output{DefaultFilter: ".root.hostname"}, Options{ResponseCodec: "xml"})
	if got != "myhost" {
		t.Fatalf("got %#v, want %q", got, "myhost")
	}
}
