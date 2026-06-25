package engine

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jedwards1230/labctl/internal/command"
	"github.com/jedwards1230/labctl/internal/manifest"
)

// fakeOp stubs the secret resolver — no real 1Password session needed.
func fakeOp(argv []string) (string, error) { return "test-key", nil }

func newService(baseURL string) *manifest.Service {
	svc := &manifest.Service{
		Name:      "radarr",
		BaseURL:   baseURL,
		EnvPrefix: "RADARR",
		Auth:      manifest.Auth{Strategy: "header-key", Header: "X-Api-Key", Value: "{secret.api_key}"},
		Secrets:   map[string]manifest.Secret{"api_key": {Ref: "op://homelab/Radarr/api_key"}},
		Commands: map[string]manifest.Command{
			"list": {Method: "GET", Path: "/api/v3/movie", Output: manifest.Output{Filter: "map(.id)"}},
		},
	}
	return svc
}

func TestExecuteEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "test-key" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != "/api/v3/movie" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":1},{"id":2}]`))
	}))
	defer srv.Close()

	svc := newService(srv.URL)
	cmds := command.FromManifest(svc)
	res, err := Execute(Request{
		Config:  manifest.Config{Secret: manifest.SecretResolver{Command: []string{"op", "read", "{ref}"}}},
		Service: svc,
		Command: cmds["list"],
		Runner:  fakeOp,
		Getenv:  func(string) string { return "" },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Body) != `[{"id":1},{"id":2}]` {
		t.Fatalf("body = %s", res.Body)
	}
	if res.Output.DefaultFilter != "map(.id)" {
		t.Fatalf("filter not resolved: %q", res.Output.DefaultFilter)
	}
}

func TestExecuteDryRunNoNetwork(t *testing.T) {
	svc := newService("https://movies.lilbro.cloud")
	cmds := command.FromManifest(svc)
	// A resolver that fails loudly — dry-run must not call it.
	failOp := func([]string) (string, error) { return "", errBoom }
	res, err := Execute(Request{
		Config:  manifest.Config{},
		Service: svc,
		Command: cmds["list"],
		Runner:  failOp,
		Flags:   Flags{DryRun: true},
		Getenv:  func(string) string { return "" },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.DryRunMsg, "GET https://movies.lilbro.cloud/api/v3/movie") {
		t.Fatalf("dry-run msg = %q", res.DryRunMsg)
	}
	if !strings.Contains(res.DryRunMsg, "X-Api-Key: <redacted>") {
		t.Fatalf("dry-run should preview a redacted auth header: %q", res.DryRunMsg)
	}
}

func TestExecuteEnvURLOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	svc := newService("https://wrong.example.com")
	cmds := command.FromManifest(svc)
	_, err := Execute(Request{
		Config:  manifest.Config{},
		Service: svc,
		Command: cmds["list"],
		Runner:  fakeOp,
		Getenv: func(k string) string {
			if k == "RADARR_URL" {
				return srv.URL
			}
			return ""
		},
	}, nil)
	if err != nil {
		t.Fatalf("RADARR_URL override should redirect to test server: %v", err)
	}
}

type boom struct{}

func (boom) Error() string { return "boom" }

var errBoom = boom{}
