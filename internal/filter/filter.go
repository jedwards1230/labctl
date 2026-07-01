// Package filter confines the gojq dependency behind a minimal jq-filter API.
// It is the ONLY package that imports github.com/itchyny/gojq directly; every
// other package (output, engine, manifest) compiles and runs jq expressions
// through this seam. The wrapper preserves gojq's exact run-once semantics —
// Compile mirrors gojq.Parse, Run mirrors query.Run's iterator, and First is the
// common "take the first result" reduction — so callers keep identical outputs,
// errors, and exit-code behavior.
package filter

import "github.com/itchyny/gojq"

// Filter is a compiled jq program, ready to run against decoded data any number
// of times.
type Filter struct {
	query *gojq.Query
}

// Compile parses a jq expression. The returned error is gojq's parse error
// (unwrapped) so callers can wrap it with their own context (e.g. "parse filter
// %q: %w"), matching the messages they produced when they called gojq directly.
func Compile(expr string) (*Filter, error) {
	q, err := gojq.Parse(expr)
	if err != nil {
		return nil, err
	}
	return &Filter{query: q}, nil
}

// Iter iterates the results of running a Filter against one input, mirroring
// gojq's iterator. gojq surfaces a runtime evaluation error as a value in the
// stream; Next splits that out into its err return so callers never handle the
// gojq error-as-value convention themselves.
type Iter struct {
	inner gojq.Iter
}

// Next returns the next result. ok is false once the stream is exhausted. A
// non-nil err is a runtime evaluation error (gojq yields these mid-stream); when
// err is non-nil ok is true and the caller should stop.
func (it *Iter) Next() (val any, err error, ok bool) {
	v, ok := it.inner.Next()
	if !ok {
		return nil, nil, false
	}
	if e, isErr := v.(error); isErr {
		return nil, e, true
	}
	return v, nil, true
}

// Run applies the filter to data and returns an iterator over every result,
// matching gojq's query.Run.
func (f *Filter) Run(data any) *Iter {
	return &Iter{inner: f.query.Run(data)}
}

// First runs the filter and returns its first result. A filter that yields no
// value returns (nil, nil); a runtime evaluation error is returned as the error.
func (f *Filter) First(data any) (any, error) {
	it := f.Run(data)
	v, err, ok := it.Next()
	if !ok {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}
