package secret

import (
	"context"
	"sort"
	"testing"

	"github.com/jedwards1230/labctl/internal/manifest"
)

func TestScrubberLongestFirst(t *testing.T) {
	// "abc" is a substring of "abcdef"; longest-first ordering must redact the
	// long value before the short one can fragment it.
	s := NewScrubber([]string{"abc", "abcdef"})
	got := s.Scrub("x abcdef y abc z")
	want := "x <redacted> y <redacted> z"
	if got != want {
		t.Fatalf("Scrub = %q, want %q", got, want)
	}
}

func TestScrubberSubstringNotPreempted(t *testing.T) {
	// The short secret must not pre-empt the longer one mid-string.
	s := NewScrubber([]string{"key", "supersecretkey"})
	if got := s.Scrub("token=supersecretkey"); got != "token=<redacted>" {
		t.Fatalf("Scrub = %q, want token=<redacted>", got)
	}
}

func TestScrubberInURLAndJSON(t *testing.T) {
	s := NewScrubber([]string{"S3CR3T"})
	url := s.Scrub("https://h/api?apikey=S3CR3T&x=1")
	if url != "https://h/api?apikey=<redacted>&x=1" {
		t.Fatalf("url scrub = %q", url)
	}
	js := s.Scrub(`{"token":"S3CR3T"}`)
	if js != `{"token":"<redacted>"}` {
		t.Fatalf("json scrub = %q", js)
	}
}

func TestScrubberEmptyAndNilIdentity(t *testing.T) {
	if got := (*Scrubber)(nil).Scrub("hello"); got != "hello" {
		t.Fatalf("nil scrubber changed text: %q", got)
	}
	if got := NewScrubber(nil).Scrub("hello"); got != "hello" {
		t.Fatalf("empty scrubber changed text: %q", got)
	}
	if got := NewScrubber([]string{"", ""}).Scrub("hello"); got != "hello" {
		t.Fatalf("scrubber built from empty values changed text: %q", got)
	}
}

func TestScrubberEmptyValueNeverBlanketReplaces(t *testing.T) {
	// A "" value must never cause a blanket replacement between every character.
	s := NewScrubber([]string{"", "real"})
	if got := s.Scrub("a real b"); got != "a <redacted> b" {
		t.Fatalf("Scrub = %q, want a <redacted> b", got)
	}
}

func TestScrubberDedupe(t *testing.T) {
	s := NewScrubber([]string{"dup", "dup", "dup"})
	if len(s.values) != 1 {
		t.Fatalf("values = %v, want one deduped entry", s.values)
	}
	if got := s.Scrub("dup dup"); got != "<redacted> <redacted>" {
		t.Fatalf("Scrub = %q", got)
	}
}

// TestResolvedValuesSnapshot proves the resolver exposes only the non-empty
// values it has actually resolved (and cached), which is what NewScrubber
// consumes to redact diagnostics.
func TestResolvedValuesSnapshot(t *testing.T) {
	run := func(argv []string) (string, error) { return "resolved-val", nil }
	r := New(context.Background(), legacyCfg(), map[string]manifest.Secret{
		"a": {Ref: "op://v/i/a"},
		"b": {Ref: "op://v/i/b"},
	}, "", run)
	r.withGetenv(func(string) string { return "" })

	// Nothing resolved yet.
	if vals := r.ResolvedValues(); len(vals) != 0 {
		t.Fatalf("ResolvedValues before any Secret() = %v, want empty", vals)
	}
	if _, err := r.Secret("a"); err != nil {
		t.Fatal(err)
	}
	vals := r.ResolvedValues()
	sort.Strings(vals)
	if len(vals) != 1 || vals[0] != "resolved-val" {
		t.Fatalf("ResolvedValues = %v, want [resolved-val]", vals)
	}
}
