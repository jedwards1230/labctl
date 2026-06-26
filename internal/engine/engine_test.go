package engine

import (
	"context"
	"encoding/json"
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
	res, err := Execute(context.Background(), Request{
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
	res, err := Execute(context.Background(), Request{
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
	_, err := Execute(context.Background(), Request{
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

// TestCursorPagination verifies that the engine accumulates items across cursor pages.
// Server returns page 1 with nextCursor "c2", page 2 with null nextCursor.
// The engine must return a merged body where .data contains items from BOTH pages.
func TestCursorPagination(t *testing.T) {
	page1 := `{"data":[{"id":1},{"id":2}],"nextCursor":"c2"}`
	page2 := `{"data":[{"id":3}],"nextCursor":null}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		switch cursor {
		case "":
			_, _ = w.Write([]byte(page1))
		case "c2":
			_, _ = w.Write([]byte(page2))
		default:
			t.Errorf("unexpected cursor %q", cursor)
			w.WriteHeader(400)
		}
	}))
	defer srv.Close()

	svc := &manifest.Service{
		Name:    "n8n",
		BaseURL: srv.URL,
		Auth:    manifest.Auth{Strategy: "none"},
		Pagination: manifest.Pagination{
			Style: "cursor",
			Param: "cursor",
			Next:  ".nextCursor",
			Data:  ".data",
		},
		Commands: map[string]manifest.Command{
			"list": {
				Method: "GET",
				Path:   "/items",
				Output: manifest.Output{Filter: "(.data // .) | map(.id)"},
			},
		},
	}
	cmds := command.FromManifest(svc)
	res, err := Execute(context.Background(), Request{
		Config:  manifest.Config{},
		Service: svc,
		Command: cmds["list"],
		Runner:  fakeOp,
		Getenv:  func(string) string { return "" },
	}, nil)
	if err != nil {
		t.Fatalf("cursor pagination: %v", err)
	}

	// Parse the synthesized body — should have {"data": [all 3 items]}.
	var body map[string]any
	if err := json.Unmarshal(res.Body, &body); err != nil {
		t.Fatalf("parse response body: %v", err)
	}
	dataRaw, ok := body["data"]
	if !ok {
		t.Fatalf("synthesized body missing 'data' key: %s", res.Body)
	}
	items, ok := dataRaw.([]any)
	if !ok {
		t.Fatalf("'data' is not an array: %T", dataRaw)
	}
	if len(items) != 3 {
		t.Fatalf("want 3 items from 2 pages, got %d: %s", len(items), res.Body)
	}
	// Verify the IDs from both pages are present.
	wantIDs := []float64{1, 2, 3}
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("item %d is not a map: %T", i, item)
		}
		id, ok := m["id"].(float64)
		if !ok {
			t.Fatalf("item %d: id not a number: %v", i, m["id"])
		}
		if id != wantIDs[i] {
			t.Fatalf("item %d: want id=%v got id=%v", i, wantIDs[i], id)
		}
	}
}

// TestCursorPaginationLimitRespected verifies that Flags.Limit stops accumulation early.
func TestCursorPaginationLimitRespected(t *testing.T) {
	page1 := `{"data":[{"id":1},{"id":2},{"id":3}],"nextCursor":"c2"}`
	page2 := `{"data":[{"id":4},{"id":5}],"nextCursor":null}`
	calls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		cursor := r.URL.Query().Get("cursor")
		if cursor == "" {
			_, _ = w.Write([]byte(page1))
		} else {
			_, _ = w.Write([]byte(page2))
		}
	}))
	defer srv.Close()

	svc := &manifest.Service{
		Name:    "test",
		BaseURL: srv.URL,
		Auth:    manifest.Auth{Strategy: "none"},
		Pagination: manifest.Pagination{
			Style: "cursor",
			Param: "cursor",
			Next:  ".nextCursor",
			Data:  ".data",
		},
		Commands: map[string]manifest.Command{
			"list": {Method: "GET", Path: "/items"},
		},
	}
	cmds := command.FromManifest(svc)
	res, err := Execute(context.Background(), Request{
		Config:  manifest.Config{},
		Service: svc,
		Command: cmds["list"],
		Flags:   Flags{Limit: 2},
		Runner:  fakeOp,
		Getenv:  func(string) string { return "" },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal(res.Body, &body); err != nil {
		t.Fatal(err)
	}
	items := body["data"].([]any)
	if len(items) != 2 {
		t.Fatalf("want 2 items (limit), got %d", len(items))
	}
	// Should have stopped after page 1 (limit reached).
	if calls != 1 {
		t.Fatalf("want 1 HTTP call (limit stops early), got %d", calls)
	}
}

// TestTrailingSlashBeforeQuery verifies that before-query appends "/" to the
// path component before the "?" query string is appended.
func TestTrailingSlashBeforeQuery(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	svc := &manifest.Service{
		Name:      "authentik",
		BaseURL:   srv.URL,
		Auth:      manifest.Auth{Strategy: "none"},
		PathRules: manifest.PathRules{TrailingSlash: "before-query"},
		Commands: map[string]manifest.Command{
			"apps": {
				Method: "GET",
				Path:   "/core/applications",
				Query:  "search=foo",
			},
		},
	}
	cmds := command.FromManifest(svc)
	_, err := Execute(context.Background(), Request{
		Config:  manifest.Config{},
		Service: svc,
		Command: cmds["apps"],
		Runner:  fakeOp,
		Getenv:  func(string) string { return "" },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Path should end in "/" (before the "?search=foo" query).
	if !strings.HasSuffix(capturedPath, "/") {
		t.Fatalf("expected path to end in /, got %q", capturedPath)
	}
	if capturedPath != "/core/applications/" {
		t.Fatalf("unexpected path %q", capturedPath)
	}
}

// TestTrailingSlashBeforeQueryAlreadyHasSlash verifies idempotence: a path that
// already ends in "/" is not doubled.
func TestTrailingSlashBeforeQueryAlreadyHasSlash(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	svc := &manifest.Service{
		Name:      "authentik",
		BaseURL:   srv.URL,
		Auth:      manifest.Auth{Strategy: "none"},
		PathRules: manifest.PathRules{TrailingSlash: "before-query"},
		Commands: map[string]manifest.Command{
			"version": {Method: "GET", Path: "/admin/version/"},
		},
	}
	cmds := command.FromManifest(svc)
	_, err := Execute(context.Background(), Request{
		Config:  manifest.Config{},
		Service: svc,
		Command: cmds["version"],
		Runner:  fakeOp,
		Getenv:  func(string) string { return "" },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if capturedPath != "/admin/version/" {
		t.Fatalf("unexpected path %q", capturedPath)
	}
}

// TestCursorPaginationNoDoubleQuery verifies Fix 1: a command with query:"active=true"
// and cursor pagination must not produce "?active=true?active=true" on the first page.
func TestCursorPaginationNoDoubleQuery(t *testing.T) {
	var capturedQueries []string

	page1 := `{"data":[{"id":1}],"nextCursor":"c2"}`
	page2 := `{"data":[{"id":2}],"nextCursor":null}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQueries = append(capturedQueries, r.URL.RawQuery)
		switch r.URL.Query().Get("cursor") {
		case "":
			_, _ = w.Write([]byte(page1))
		case "c2":
			_, _ = w.Write([]byte(page2))
		default:
			t.Errorf("unexpected cursor %q", r.URL.Query().Get("cursor"))
			w.WriteHeader(400)
		}
	}))
	defer srv.Close()

	svc := &manifest.Service{
		Name:    "n8n",
		BaseURL: srv.URL,
		Auth:    manifest.Auth{Strategy: "none"},
		Pagination: manifest.Pagination{
			Style: "cursor",
			Param: "cursor",
			Next:  ".nextCursor",
			Data:  ".data",
		},
		Commands: map[string]manifest.Command{
			"list": {
				Method: "GET",
				Path:   "/workflows",
				Query:  "active=true",
				Output: manifest.Output{Filter: "(.data // .) | map(.id)"},
			},
		},
	}
	cmds := command.FromManifest(svc)
	_, err := Execute(context.Background(), Request{
		Config:  manifest.Config{},
		Service: svc,
		Command: cmds["list"],
		Runner:  fakeOp,
		Getenv:  func(string) string { return "" },
	}, nil)
	if err != nil {
		t.Fatalf("cursor pagination with query: %v", err)
	}

	if len(capturedQueries) != 2 {
		t.Fatalf("want 2 HTTP calls, got %d", len(capturedQueries))
	}
	// First page: active=true only (no cursor), second: active=true&cursor=c2.
	// In each case "active=true" must appear exactly once.
	for i, q := range capturedQueries {
		count := strings.Count(q, "active=true")
		if count != 1 {
			t.Errorf("page %d RawQuery %q: 'active=true' appears %d times, want exactly 1", i+1, q, count)
		}
	}
}

// TestExtractDataNullValue verifies Fix 4: a response with a non-trivial data path
// that returns null produces an error rather than silently stopping pagination.
func TestExtractDataNullValue(t *testing.T) {
	body := []byte(`{"data":null,"nextCursor":"abc"}`)
	_, err := extractData(body, ".data")
	if err == nil {
		t.Fatal("expected error for null data value with dataPath=.data, got nil")
	}
	if !strings.Contains(err.Error(), "null") {
		t.Errorf("error %q should mention null", err.Error())
	}
}

// TestExtractDataWholeBodyNull verifies that a null body with no dataPath (filter=".")
// is treated as empty rather than an error (pagination stops naturally).
func TestExtractDataWholeBodyNull(t *testing.T) {
	body := []byte(`null`)
	items, err := extractData(body, "")
	if err != nil {
		t.Fatalf("unexpected error for null body with empty dataPath: %v", err)
	}
	if items != nil {
		t.Fatalf("expected nil items for null body, got %v", items)
	}
}

// TestExecuteDryRun_DoesNotReadSecretToken proves a dry-run never resolves
// secrets and so never touches a (here, nonexistent) service-account token file.
// With Runner:nil the real op path would lazily read the token — but dry-run
// short-circuits before any resolution, so the preview is produced cleanly.
func TestExecuteDryRun_DoesNotReadSecretToken(t *testing.T) {
	svc := newService("https://movies.lilbro.cloud")
	cmds := command.FromManifest(svc)
	cfg := manifest.Config{
		Secrets: manifest.SecretsConfig{
			Providers: map[string]manifest.ProviderConfig{
				"onepassword": {
					Scheme:  "op",
					Command: []string{"op", "read", "{ref}"},
					Auth: manifest.ProviderAuth{
						ServiceAccountToken: &manifest.SecretSource{File: "/nonexistent/sa-token"},
					},
				},
			},
		},
	}
	res, err := Execute(context.Background(), Request{
		Config:  cfg,
		Service: svc,
		Command: cmds["list"],
		Runner:  nil, // real op path — but dry-run must not invoke it
		Flags:   Flags{DryRun: true},
		Getenv:  func(string) string { return "" },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.DryRunMsg, "GET https://movies.lilbro.cloud/api/v3/movie") {
		t.Fatalf("dry-run msg = %q", res.DryRunMsg)
	}
}

type boom struct{}

func (boom) Error() string { return "boom" }

var errBoom = boom{}
