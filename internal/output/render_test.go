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
