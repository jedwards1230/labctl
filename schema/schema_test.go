package schema_test

import (
	"bytes"
	"testing"

	"github.com/jedwards1230/labctl/catalog"
	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const schemaURL = "https://raw.githubusercontent.com/jedwards1230/labctl/main/schema/manifest.schema.json"

// compileSchema compiles the embedded draft-07 schema once per test. The schema
// JSON is added as a resource under its $id so $ref/#/definitions resolve.
func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema.Manifest))
	if err != nil {
		t.Fatalf("unmarshal embedded schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaURL, doc); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	sch, err := c.Compile(schemaURL)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return sch
}

// schemaValidate YAML-unmarshals a manifest into a generic value (yaml.v3 yields
// map[string]interface{} keys, which the validator accepts) and validates it
// against the compiled schema.
func schemaValidate(sch *jsonschema.Schema, src []byte) error {
	var v interface{}
	if err := yaml.Unmarshal(src, &v); err != nil {
		return err
	}
	return sch.Validate(v)
}

// goValidate decodes a manifest into manifest.Service and runs the structural
// Validate — the Go-side counterpart of the schema check, used by the agreement
// test. Note: decodeService uses plain yaml.Unmarshal (not KnownFields), so this
// mirrors how manifests actually load.
func goValidate(src []byte) error {
	var svc manifest.Service
	if err := yaml.Unmarshal(src, &svc); err != nil {
		return err
	}
	return manifest.Validate(&svc)
}

// TestCatalogManifestsConformToSchema is the no-false-positives guarantee: every
// embedded catalog manifest must validate clean against the schema AND pass the
// Go structural Validate. (internal/manifest/testdata/petstore.yaml is excluded
// on purpose — it is an OpenAPI 3.0 document used as a spec: inference fixture,
// not a labctl manifest, so it would correctly fail this schema.)
func TestCatalogManifestsConformToSchema(t *testing.T) {
	sch := compileSchema(t)
	names := catalog.Names()
	if len(names) == 0 {
		t.Fatal("no catalog manifests found")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			data, ok := catalog.Manifest(name)
			if !ok {
				t.Fatalf("catalog.Manifest(%q) missing", name)
			}
			if err := schemaValidate(sch, data); err != nil {
				t.Errorf("schema rejected catalog manifest %q: %v", name, err)
			}
			var svc manifest.Service
			if err := yaml.Unmarshal(data, &svc); err != nil {
				t.Fatalf("yaml decode %q: %v", name, err)
			}
			if err := manifest.Validate(&svc); err != nil {
				t.Errorf("manifest.Validate rejected catalog manifest %q: %v", name, err)
			}
		})
	}
	t.Logf("conformance corpus: %d catalog manifests", len(names))
}

// TestKnownInvalidManifestsFailSchema asserts the schema catches the structural
// rules it is responsible for: forbidden in-manifest bindings, bad enums,
// conditional required auth fields, unknown keys, and type mismatches.
func TestKnownInvalidManifestsFailSchema(t *testing.T) {
	sch := compileSchema(t)
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "service base_url forbidden",
			yaml: "name: x\nbase_url: https://example.test\n",
		},
		{
			name: "secret ref forbidden",
			yaml: "name: x\nsecrets:\n  token:\n    ref: op://vault/item/field\n",
		},
		{
			name: "unknown auth strategy",
			yaml: "name: x\nauth:\n  strategy: magic\n",
		},
		{
			name: "unknown pagination style",
			yaml: "name: x\npagination:\n  style: infinite\n",
		},
		{
			name: "header-key missing header",
			yaml: "name: x\nauth:\n  strategy: header-key\n  value: \"{secret.k}\"\n",
		},
		{
			name: "bearer missing value",
			yaml: "name: x\nauth:\n  strategy: bearer\n",
		},
		{
			name: "basic missing username",
			yaml: "name: x\nauth:\n  strategy: basic\n  password: p\n",
		},
		{
			name: "basic missing password",
			yaml: "name: x\nauth:\n  strategy: basic\n  username: u\n",
		},
		{
			name: "oauth2-client-credentials missing token_url and value",
			yaml: "name: x\nauth:\n  strategy: oauth2-client-credentials\n  client_id: c\n  client_secret: s\n",
		},
		{
			name: "oauth2-client-credentials missing client_id and username",
			yaml: "name: x\nauth:\n  strategy: oauth2-client-credentials\n  token_url: t\n  client_secret: s\n",
		},
		{
			name: "oauth2-client-credentials missing client_secret and password",
			yaml: "name: x\nauth:\n  strategy: oauth2-client-credentials\n  token_url: t\n  client_id: c\n",
		},
		{
			name: "unknown top-level key",
			yaml: "name: x\nbaseurl: https://example.test\n",
		},
		{
			name: "tls_insecure wrong type",
			yaml: "name: x\ntls_insecure: notabool\n",
		},
		{
			name: "endpoint base_url forbidden",
			yaml: "name: x\nendpoints:\n  alt:\n    base_url: https://example.test\n",
		},
		{
			name: "ui.view bad enum",
			yaml: "name: x\ncommands:\n  list:\n    method: GET\n    path: /list\n    ui:\n      view: chart\n",
		},
		{
			name: "ui.sort.dir bad enum",
			yaml: "name: x\ncommands:\n  list:\n    method: GET\n    path: /list\n    ui:\n      sort:\n        dir: sideways\n",
		},
		{
			name: "ui unknown key",
			yaml: "name: x\ncommands:\n  list:\n    method: GET\n    path: /list\n    ui:\n      bogus: true\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := schemaValidate(sch, []byte(tc.yaml)); err == nil {
				t.Errorf("schema accepted known-invalid manifest %q; want rejection", tc.name)
			}
		})
	}
}

// TestSchemaAndValidateAgree exercises ONLY the structural rules that both the
// JSON Schema and the Go Validate enforce, asserting both engines reach the same
// verdict. This catches drift between the hand-authored schema and the Go model
// on the overlapping rules.
//
// Known, intentional divergences NOT covered here (the test would flap on them):
//   - The schema is additively stricter: it rejects unknown keys, bad codec
//     values, and type mismatches that Go's non-strict yaml.Unmarshal silently
//     drops (decodeService does NOT use KnownFields).
//   - Validate is stricter on semantic rules the schema cannot express:
//     undeclared secret references, jq validity, spec reachability, and the
//     transport-dependent path requirement.
//
// Every fixture below uses only rules in the overlap, and declares any referenced
// secret so the undeclared-secret rule never causes divergence.
func TestSchemaAndValidateAgree(t *testing.T) {
	sch := compileSchema(t)
	cases := []struct {
		name      string
		yaml      string
		wantValid bool
	}{
		{
			name:      "service base_url present (both reject)",
			yaml:      "name: x\nbase_url: https://example.test\n",
			wantValid: false,
		},
		{
			name:      "secret ref present (both reject)",
			yaml:      "name: x\nsecrets:\n  token:\n    ref: op://v/i/f\n",
			wantValid: false,
		},
		{
			name:      "unknown auth strategy (both reject)",
			yaml:      "name: x\nauth:\n  strategy: nope\n",
			wantValid: false,
		},
		{
			name:      "unknown pagination style (both reject)",
			yaml:      "name: x\npagination:\n  style: infinite\n",
			wantValid: false,
		},
		{
			name:      "header-key missing header (both reject)",
			yaml:      "name: x\nauth:\n  strategy: header-key\n  value: \"{secret.k}\"\nsecrets:\n  k:\n    env: X_K\n",
			wantValid: false,
		},
		{
			name: "clean bearer manifest (both accept)",
			yaml: "name: x\nenv_prefix: X\nauth:\n  strategy: bearer\n  value: \"{secret.token}\"\n" +
				"secrets:\n  token:\n    env: X_TOKEN\ncommands:\n  ping:\n    method: GET\n    path: /ping\n",
			wantValid: true,
		},
		{
			name: "clean header-key manifest (both accept)",
			yaml: "name: y\nauth:\n  strategy: header-key\n  header: X-API-KEY\n  value: \"{secret.api_key}\"\n" +
				"secrets:\n  api_key:\n    env: Y_API_KEY\ncommands:\n  list:\n    method: GET\n    path: /list\n",
			wantValid: true,
		},
		// Empty-string conditional auth fields: validateAuth rejects an empty
		// required field, and the schema's minLength:1 (inside the then-clause)
		// must agree. These lock the minLength against drift.
		{
			name:      "bearer empty value (both reject)",
			yaml:      "name: x\nauth:\n  strategy: bearer\n  value: \"\"\n",
			wantValid: false,
		},
		{
			name:      "basic empty password (both reject)",
			yaml:      "name: x\nauth:\n  strategy: basic\n  username: u\n  password: \"\"\n",
			wantValid: false,
		},
		// basic conditional required fields.
		{
			name:      "basic complete (both accept)",
			yaml:      "name: x\nauth:\n  strategy: basic\n  username: u\n  password: p\n",
			wantValid: true,
		},
		{
			name:      "basic missing password (both reject)",
			yaml:      "name: x\nauth:\n  strategy: basic\n  username: u\n",
			wantValid: false,
		},
		// oauth2-client-credentials: the allOf/anyOf fallback pairs are the
		// likeliest to drift, so cover both the intent-revealing fields and the
		// overloaded value/username/password fallback, plus a missing field.
		{
			name:      "oauth2 intent fields (both accept)",
			yaml:      "name: x\nauth:\n  strategy: oauth2-client-credentials\n  token_url: https://t/token\n  client_id: id\n  client_secret: shh\n",
			wantValid: true,
		},
		{
			name:      "oauth2 overloaded fallback fields (both accept)",
			yaml:      "name: x\nauth:\n  strategy: oauth2-client-credentials\n  value: https://t/token\n  username: id\n  password: shh\n",
			wantValid: true,
		},
		{
			name:      "oauth2 missing client_secret (both reject)",
			yaml:      "name: x\nauth:\n  strategy: oauth2-client-credentials\n  token_url: https://t/token\n  client_id: id\n",
			wantValid: false,
		},
		// transport enum.
		{
			name:      "unknown transport (both reject)",
			yaml:      "name: x\ntransport: ftp\n",
			wantValid: false,
		},
		{
			name:      "jsonrpc-ws transport (both accept)",
			yaml:      "name: x\ntransport: jsonrpc-ws\ncommands:\n  ping:\n    method: core.ping\n",
			wantValid: true,
		},
		// output mode enum.
		{
			name:      "unknown output mode (both reject)",
			yaml:      "name: x\noutput:\n  mode: fancy\n",
			wantValid: false,
		},
		// secret idiom enum.
		{
			name:      "unknown secret idiom (both reject)",
			yaml:      "name: x\nsecrets:\n  k:\n    idiom: weird\n    env: X_K\n",
			wantValid: false,
		},
		{
			name: "valid secret idiom (both accept)",
			yaml: "name: x\nauth:\n  strategy: bearer\n  value: \"{secret.token}\"\n" +
				"secrets:\n  token:\n    idiom: item-get\n    env: X_TOKEN\ncommands:\n  ping:\n    method: GET\n    path: /p\n",
			wantValid: true,
		},
		// endpoint base_url is forbidden in a manifest (both reject).
		{
			name:      "endpoint base_url present (both reject)",
			yaml:      "name: x\nendpoints:\n  alt:\n    base_url: https://example.test\n",
			wantValid: false,
		},
		// ui: hint block (Phase 2): clean block accepted, bad view enum rejected.
		{
			name: "clean ui block (both accept)",
			yaml: "name: x\ncommands:\n  list:\n    method: GET\n    path: /list\n" +
				"    ui:\n      view: table\n      columns: [id, name]\n      primary: name\n" +
				"      sort:\n        by: name\n        dir: asc\n      drilldown: get\n",
			wantValid: true,
		},
		{
			name:      "ui.view bad enum (both reject)",
			yaml:      "name: x\ncommands:\n  list:\n    method: GET\n    path: /list\n    ui:\n      view: chart\n",
			wantValid: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schemaErr := schemaValidate(sch, []byte(tc.yaml))
			if (schemaErr == nil) != tc.wantValid {
				t.Errorf("schema valid=%v, want %v (err=%v)", schemaErr == nil, tc.wantValid, schemaErr)
			}
			goErr := goValidate([]byte(tc.yaml))
			if (goErr == nil) != tc.wantValid {
				t.Errorf("Validate valid=%v, want %v (err=%v)", goErr == nil, tc.wantValid, goErr)
			}
		})
	}
}
