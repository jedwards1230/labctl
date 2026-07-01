package filter

import (
	"reflect"
	"testing"
)

func TestCompileRejectsInvalidExpr(t *testing.T) {
	if _, err := Compile("."); err != nil {
		t.Fatalf("Compile(.) = %v, want nil", err)
	}
	if _, err := Compile("this is not | valid ("); err == nil {
		t.Fatal("Compile of a malformed expression = nil, want a parse error")
	}
}

func TestFirst(t *testing.T) {
	tests := []struct {
		name string
		expr string
		in   any
		want any
	}{
		{"identity", ".", map[string]any{"a": float64(1)}, map[string]any{"a": float64(1)}},
		{"field", ".name", map[string]any{"name": "widget"}, "widget"},
		{"first of many", ".[]", []any{"a", "b", "c"}, "a"},
		{"explicit null", ".missing", map[string]any{}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Compile(tc.expr)
			if err != nil {
				t.Fatalf("Compile(%q): %v", tc.expr, err)
			}
			got, err := f.First(tc.in)
			if err != nil {
				t.Fatalf("First: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("First = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestFirstNoResults(t *testing.T) {
	// `empty` yields no value at all — distinct from a null result.
	f, err := Compile("empty")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	got, err := f.First(map[string]any{"a": float64(1)})
	if err != nil {
		t.Fatalf("First: %v", err)
	}
	if got != nil {
		t.Fatalf("First over empty = %#v, want nil", got)
	}
}

func TestFirstRuntimeError(t *testing.T) {
	// Indexing a string with a field access is a runtime evaluation error that
	// gojq yields mid-stream; First must surface it as an error.
	f, err := Compile(".a")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if _, err := f.First("not-an-object"); err == nil {
		t.Fatal("First over a type error = nil, want a runtime error")
	}
}

func TestRunIteratesAllResults(t *testing.T) {
	f, err := Compile(".[]")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	var got []any
	it := f.Run([]any{"a", "b", "c"})
	for {
		v, ferr, ok := it.Next()
		if !ok {
			break
		}
		if ferr != nil {
			t.Fatalf("Next: %v", ferr)
		}
		got = append(got, v)
	}
	want := []any{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Run results = %#v, want %#v", got, want)
	}
}

func TestRunSurfacesRuntimeError(t *testing.T) {
	f, err := Compile(".a")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	_, ferr, ok := f.Run(42).Next()
	if !ok {
		t.Fatal("Next over a runtime error = !ok, want ok with an error")
	}
	if ferr == nil {
		t.Fatal("Next err = nil, want a runtime evaluation error")
	}
}
