package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestSchemaCommand: `labctl schema` emits the embedded JSON Schema as valid JSON
// containing the draft-07 $schema declaration.
func TestSchemaCommand(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := Run([]string{"schema"}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, `"$schema"`) {
		t.Fatalf("schema output missing $schema declaration: %q", got)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("schema output is not valid JSON: %v", err)
	}
	if s, _ := doc["$schema"].(string); !strings.Contains(s, "draft-07") {
		t.Fatalf("$schema = %q, want a draft-07 URI", s)
	}
}
