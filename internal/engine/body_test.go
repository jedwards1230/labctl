package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedwards1230/labctl/internal/command"
	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/template"
)

// TestResolveBodyInlineJSON verifies an inline body is template-expanded and
// defaults to the application/json content type. JSON object braces pass through
// while {var} tokens expand.
func TestResolveBodyInlineJSON(t *testing.T) {
	cmd := &command.Command{Body: `{"pool":"{poolname}"}`}
	env := template.Env{Vars: map[string]string{"poolname": "tank"}}

	body, ct, err := resolveBody(cmd, env)
	if err != nil {
		t.Fatalf("resolveBody: %v", err)
	}
	if string(body) != `{"pool":"tank"}` {
		t.Fatalf("body = %s, want {\"pool\":\"tank\"}", body)
	}
	if ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
}

// TestResolveBodyEmpty verifies an empty body yields nil bytes and no content
// type (so no header is forced for bodiless requests).
func TestResolveBodyEmpty(t *testing.T) {
	body, ct, err := resolveBody(&command.Command{}, template.Env{})
	if err != nil {
		t.Fatalf("resolveBody: %v", err)
	}
	if body != nil {
		t.Fatalf("body = %s, want nil", body)
	}
	if ct != "" {
		t.Fatalf("content-type = %q, want empty", ct)
	}
}

// TestResolveBodyFormCodec verifies the form request codec selects the
// urlencoded content type.
func TestResolveBodyFormCodec(t *testing.T) {
	cmd := &command.Command{
		Body:  "grant_type=client_credentials",
		Codec: manifest.Codec{Request: "form"},
	}
	body, ct, err := resolveBody(cmd, template.Env{})
	if err != nil {
		t.Fatalf("resolveBody: %v", err)
	}
	if string(body) != "grant_type=client_credentials" {
		t.Fatalf("body = %s", body)
	}
	if ct != "application/x-www-form-urlencoded" {
		t.Fatalf("content-type = %q, want application/x-www-form-urlencoded", ct)
	}
}

// TestResolveBodyFileSuccess verifies an @file body reads the file contents
// verbatim (no template expansion of file contents).
func TestResolveBodyFileSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.json")
	const want = `{"name":"from-file","n":42}`
	if err := os.WriteFile(path, []byte(want), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := &command.Command{Body: "@" + path}
	body, ct, err := resolveBody(cmd, template.Env{})
	if err != nil {
		t.Fatalf("resolveBody: %v", err)
	}
	if string(body) != want {
		t.Fatalf("body = %s, want %s", body, want)
	}
	if ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
}

// TestResolveBodyFileReadError verifies a missing @file body surfaces a wrapped
// read error that names the path.
func TestResolveBodyFileReadError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	cmd := &command.Command{Body: "@" + missing}

	_, _, err := resolveBody(cmd, template.Env{})
	if err == nil {
		t.Fatal("expected error for missing @file body, got nil")
	}
	if !strings.Contains(err.Error(), "read body file") {
		t.Fatalf("error = %q, want it to mention 'read body file'", err.Error())
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error = %q, want it to name the missing path", err.Error())
	}
}
