package template

import "testing"

type fakeResolver map[string]string

func (f fakeResolver) Secret(name string) (string, error) {
	if v, ok := f[name]; ok {
		return v, nil
	}
	return "", errNotFound{name}
}

type errNotFound struct{ name string }

func (e errNotFound) Error() string { return "no secret " + e.name }

func TestExpand(t *testing.T) {
	env := Env{
		Vars:    map[string]string{"host": "192.0.2.10"},
		Args:    []string{"abc", "42"},
		Secrets: fakeResolver{"api_key": "s3cr3t"},
		Getenv:  func(k string) string { return map[string]string{"FOO": "bar"}[k] },
	}
	cases := []struct {
		in, want string
	}{
		{"https://{host}:47990/api", "https://192.0.2.10:47990/api"},
		{"{secret.api_key}", "s3cr3t"},
		{"X-{env.FOO}-Y", "X-bar-Y"},
		{"/movie/{arg.0}", "/movie/abc"},
		{"/movie/{arg.1}", "/movie/42"},
		{"plain text", "plain text"},
		// JSON bodies must pass through untouched (the brace-overload fix).
		{`{"data":{"collection":"X","mode":"getAll"}}`, `{"data":{"collection":"X","mode":"getAll"}}`},
		{`{"k":"{secret.api_key}"}`, `{"k":"s3cr3t"}`},
	}
	for _, c := range cases {
		got, err := env.Expand(c.in)
		if err != nil {
			t.Errorf("Expand(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Expand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExpandErrors(t *testing.T) {
	env := Env{Vars: map[string]string{}, Secrets: fakeResolver{}}
	for _, in := range []string{"{unknownvar}", "{secret.missing}", "{arg.5}"} {
		if _, err := env.Expand(in); err == nil {
			t.Errorf("Expand(%q) expected error, got nil", in)
		}
	}
}

func TestExpandNoSecretResolver(t *testing.T) {
	env := Env{Vars: map[string]string{}}
	if _, err := env.Expand("{secret.x}"); err == nil {
		t.Error("expected error when no resolver configured")
	}
}

// TestArgPrefixedVarsResolve proves a var whose name merely starts with "arg"
// (args, argument, arg_region) resolves as a var, while genuine positional-arg
// tokens (arg0, arg.1) still resolve as args.
func TestArgPrefixedVarsResolve(t *testing.T) {
	env := Env{
		Vars: map[string]string{"args": "A", "argument": "B", "arg_region": "us"},
		Args: []string{"zero", "one"},
	}
	cases := map[string]string{
		"{args}":       "A",
		"{argument}":   "B",
		"{arg_region}": "us",
		"{arg0}":       "zero", // genuine arg tokens still work
		"{arg.1}":      "one",
	}
	for in, want := range cases {
		got, err := env.Expand(in)
		if err != nil {
			t.Fatalf("Expand(%q) error: %v", in, err)
		}
		if got != want {
			t.Errorf("Expand(%q) = %q, want %q", in, got, want)
		}
	}
}
