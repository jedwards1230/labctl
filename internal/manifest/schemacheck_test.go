package manifest

import (
	"errors"
	"testing"
)

// TestSchemaValidateAcceptsPortable: a clean portable manifest passes both the
// schema and the structural Validate via ValidatePortableManifest, returning the
// declared name.
func TestSchemaValidateAcceptsPortable(t *testing.T) {
	const good = "name: widget\nauth: { strategy: none }\ncommands:\n  list: { method: GET, path: /list }\n"
	if err := SchemaValidate([]byte(good)); err != nil {
		t.Fatalf("SchemaValidate rejected a clean manifest: %v", err)
	}
	name, err := ValidatePortableManifest([]byte(good))
	if err != nil {
		t.Fatalf("ValidatePortableManifest: %v", err)
	}
	if name != "widget" {
		t.Errorf("name = %q, want widget", name)
	}
}

// TestValidatePortableManifestRejectsBindings: a manifest carrying a base_url or a
// secret ref is non-portable — the schema and Validate both reject it, and the
// error classifies as a *ConfigError (exit 2).
func TestValidatePortableManifestRejectsBindings(t *testing.T) {
	cases := map[string]string{
		"base_url":          "name: x\nbase_url: https://h.example\nauth: { strategy: none }\n",
		"secret ref":        "name: x\nsecrets:\n  token:\n    ref: op://v/i/f\n",
		"endpoint base_url": "name: x\nendpoints:\n  alt:\n    base_url: https://h.example\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ValidatePortableManifest([]byte(body))
			if err == nil {
				t.Fatalf("ValidatePortableManifest accepted a binding-carrying manifest")
			}
			var cfgErr *ConfigError
			if !errors.As(err, &cfgErr) {
				t.Errorf("want *ConfigError (exit 2), got %T: %v", err, err)
			}
		})
	}
}

// TestSchemaValidateRejectsNonSchema: a manifest with an unknown top-level key or
// a bad enum is caught by the schema (which is additively stricter than Validate).
func TestSchemaValidateRejectsNonSchema(t *testing.T) {
	cases := map[string]string{
		"unknown key":       "name: x\nbaseurl: https://h.example\n",
		"bad auth enum":     "name: x\nauth: { strategy: magic }\n",
		"bad output mode":   "name: x\noutput: { mode: fancy }\n",
		"wrong scalar type": "name: x\ntls_insecure: notabool\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if err := SchemaValidate([]byte(body)); err == nil {
				t.Errorf("SchemaValidate accepted a non-conformant manifest")
			}
		})
	}
}
