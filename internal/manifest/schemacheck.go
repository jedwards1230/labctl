package manifest

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/jedwards1230/labctl/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// manifestSchemaURL is the $id the embedded schema is registered under so its
// internal $ref/#/definitions resolve. It mirrors schema/schema_test.go.
const manifestSchemaURL = "https://raw.githubusercontent.com/jedwards1230/labctl/main/schema/manifest.schema.json"

var (
	compiledSchemaOnce sync.Once
	compiledSchema     *jsonschema.Schema
	compiledSchemaErr  error
)

// manifestSchema compiles the embedded draft-07 schema exactly once (the compile
// is pure and the schema never changes at runtime), returning the cached result.
func manifestSchema() (*jsonschema.Schema, error) {
	compiledSchemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema.Manifest))
		if err != nil {
			compiledSchemaErr = fmt.Errorf("unmarshal embedded schema: %w", err)
			return
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource(manifestSchemaURL, doc); err != nil {
			compiledSchemaErr = fmt.Errorf("add schema resource: %w", err)
			return
		}
		sch, err := c.Compile(manifestSchemaURL)
		if err != nil {
			compiledSchemaErr = fmt.Errorf("compile schema: %w", err)
			return
		}
		compiledSchema = sch
	})
	return compiledSchema, compiledSchemaErr
}

// SchemaValidate validates one manifest's raw bytes against the embedded draft-07
// JSON Schema (the same schema `labctl schema` prints and editors consume). A
// schema violation is wrapped in *ConfigError (exit 2). yaml.v3 yields
// map[string]interface{} keys, which the validator accepts.
func SchemaValidate(data []byte) error {
	sch, err := manifestSchema()
	if err != nil {
		return err
	}
	var v interface{}
	if err := yaml.Unmarshal(data, &v); err != nil {
		return &ConfigError{Err: fmt.Errorf("parse manifest: %w", err)}
	}
	if err := sch.Validate(v); err != nil {
		return &ConfigError{Err: fmt.Errorf("schema: %w", err)}
	}
	return nil
}

// ValidatePortableManifest is the validate-on-add gate for a catalog manifest: it
// enforces BOTH the JSON Schema and the structural Validate. Validate is the
// portability boundary — it rejects an in-manifest base_url or secret ref, the
// security property that keeps an installed catalog inert (no endpoints/creds)
// until profile.yaml binds it. It runs no spec inference and touches no network.
// The resolved service name (svc.Name, empty if unset) is returned so the caller
// can pre-detect duplicate service names within one source.
func ValidatePortableManifest(data []byte) (name string, err error) {
	if err := SchemaValidate(data); err != nil {
		return "", err
	}
	var svc Service
	if err := yaml.Unmarshal(data, &svc); err != nil {
		return "", &ConfigError{Err: fmt.Errorf("parse manifest: %w", err)}
	}
	if err := Validate(&svc); err != nil {
		return "", err
	}
	return svc.Name, nil
}
