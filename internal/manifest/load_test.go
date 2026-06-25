package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, dir, name, body string) {
	t.Helper()
	svcDir := filepath.Join(dir, "services")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svcDir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMergeDefaults(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "tdarr.yaml", `
base_url: http://tdarr.local
auth: { strategy: none }
`)
	l, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc, ok := l.Services["tdarr"]
	if !ok {
		t.Fatal("tdarr not loaded (name should default to filename stem)")
	}
	if svc.Transport != "http" {
		t.Errorf("transport default = %q, want http", svc.Transport)
	}
	if svc.Timeout != "60s" {
		t.Errorf("timeout default = %q, want 60s", svc.Timeout)
	}
	if svc.TimeoutDuration().Seconds() != 60 {
		t.Errorf("timeout = %v, want 60s", svc.TimeoutDuration())
	}
}

func TestLoadMissingDir(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(l.Services) != 0 {
		t.Error("expected zero services")
	}
}

func TestValidateRejectsBad(t *testing.T) {
	cases := map[string]*Service{
		"no base_url":          {Name: "x"},
		"bad transport":        {Name: "x", BaseURL: "http://h", Transport: "carrier-pigeon"},
		"bad strategy":         {Name: "x", BaseURL: "http://h", Auth: Auth{Strategy: "telepathy"}},
		"undeclared secret":    {Name: "x", BaseURL: "http://h", Auth: Auth{Strategy: "header-key", Header: "X", Value: "{secret.missing}"}},
		"header-key no header": {Name: "x", BaseURL: "http://h", Auth: Auth{Strategy: "header-key", Value: "v"}},
	}
	for name, svc := range cases {
		if err := Validate(svc); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestValidateAcceptsGood(t *testing.T) {
	svc := &Service{
		Name:    "radarr",
		BaseURL: "https://movies.lilbro.cloud",
		Auth:    Auth{Strategy: "header-key", Header: "X-Api-Key", Value: "{secret.api_key}"},
		Secrets: map[string]Secret{"api_key": {Ref: "op://homelab/Radarr/api_key"}},
		Commands: map[string]Command{
			"status": {Method: "GET", Path: "/api/v3/system/status"},
		},
	}
	if err := Validate(svc); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
}

func TestSecretRefsExtraction(t *testing.T) {
	got := secretRefs(`{secret.username}:{secret.password}`)
	if len(got) != 2 || got[0] != "username" || got[1] != "password" {
		t.Fatalf("got %v", got)
	}
}
