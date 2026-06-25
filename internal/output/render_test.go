package output

import (
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

func TestRenderDefaultFilter(t *testing.T) {
	got := render(t, `{"a":1,"b":2}`, manifest.Output{DefaultFilter: ".a"}, Options{})
	if strings.TrimSpace(got) != "1" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderFilterOverride(t *testing.T) {
	got := render(t, `{"a":1,"b":2}`, manifest.Output{DefaultFilter: ".a"}, Options{Filter: ".b"})
	if strings.TrimSpace(got) != "2" {
		t.Fatalf("got %q", got)
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
