package manifest

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// petstoreFixture returns the absolute path to the petstore fixture.
func petstoreFixture(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("testdata/petstore.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// ──────────────────────────────────────────────────────────────────────────────
// slugify

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"listPets", "listpets"},
		{"getPetById", "getpetbyid"},
		{"List Owners", "list-owners"},
		// Space and slash are both non-alnum; the regex collapses runs to a single "-".
		{"GET /api/v1/pets", "get-api-v1-pets"},
		{"--foo--", "foo"},
	}
	for _, c := range cases {
		if got := slugify(c.in); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// parseOperations + InferredCommands (file-based)

func TestParseOperationsCount(t *testing.T) {
	b, err := os.ReadFile("testdata/petstore.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ops, err := parseOperations(b)
	if err != nil {
		t.Fatalf("parseOperations: %v", err)
	}
	// Fixture has 7 operations: GET/POST /pets, GET/DELETE /pets/{petId},
	// GET /owners, GET /owners/{ownerId}/pets, GET /health.
	const want = 7
	if len(ops) != want {
		t.Errorf("got %d operations, want %d", len(ops), want)
	}
}

func TestInferredCommandsShape(t *testing.T) {
	fix := petstoreFixture(t)
	svc := &Service{
		Name:    "petstore",
		BaseURL: "http://petstore.example",
		Spec:    fix,
	}
	cmds, err := InferredCommands(svc, filepath.Dir(fix))
	if err != nil {
		t.Fatalf("InferredCommands: %v", err)
	}
	// 7 operations → 7 commands (no filter).
	if len(cmds) != 7 {
		t.Errorf("got %d commands, want 7; keys: %v", len(cmds), cmdKeys(cmds))
	}

	// operationId slug is used as key.
	listPets, ok := cmds["listpets"]
	if !ok {
		t.Fatalf("expected key 'listpets', got keys: %v", cmdKeys(cmds))
	}
	if listPets.Method != "GET" {
		t.Errorf("listpets.Method = %q, want GET", listPets.Method)
	}
	if listPets.Path != "/pets" {
		t.Errorf("listpets.Path = %q, want /pets", listPets.Path)
	}
	if listPets.Help != "List all pets" {
		t.Errorf("listpets.Help = %q, want 'List all pets'", listPets.Help)
	}
}

func TestInferredCommandsFallbackKey(t *testing.T) {
	// The /health GET has no operationId → key derived from method+path.
	fix := petstoreFixture(t)
	svc := &Service{
		Name:    "petstore",
		BaseURL: "http://petstore.example",
		Spec:    fix,
	}
	cmds, err := InferredCommands(svc, filepath.Dir(fix))
	if err != nil {
		t.Fatalf("InferredCommands: %v", err)
	}
	// fallback key: slugify("get-" + "/health") = "get-health"
	// ("-" + "/" collapse to a single "-" via the non-alnum regex)
	if _, ok := cmds["get-health"]; !ok {
		t.Errorf("expected fallback key 'get-health' (no operationId), keys: %v", cmdKeys(cmds))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// SpecFilter: Include/Exclude

func TestSpecFilterExcludeMethod(t *testing.T) {
	fix := petstoreFixture(t)
	svc := &Service{
		Name:    "petstore",
		BaseURL: "http://petstore.example",
		Spec:    fix,
		SpecFilter: SpecFilter{
			Exclude: []string{"DELETE", "POST"},
		},
	}
	cmds, err := InferredCommands(svc, filepath.Dir(fix))
	if err != nil {
		t.Fatalf("InferredCommands: %v", err)
	}
	for key, cmd := range cmds {
		if cmd.Method == "DELETE" || cmd.Method == "POST" {
			t.Errorf("excluded method %s still present as key %q", cmd.Method, key)
		}
	}
	// 7 total - 1 DELETE - 1 POST = 5.
	if len(cmds) != 5 {
		t.Errorf("got %d commands after exclude, want 5; keys: %v", len(cmds), cmdKeys(cmds))
	}
}

func TestSpecFilterIncludeTag(t *testing.T) {
	fix := petstoreFixture(t)
	svc := &Service{
		Name:    "petstore",
		BaseURL: "http://petstore.example",
		Spec:    fix,
		SpecFilter: SpecFilter{
			Include: []string{"owners"},
		},
	}
	cmds, err := InferredCommands(svc, filepath.Dir(fix))
	if err != nil {
		t.Fatalf("InferredCommands: %v", err)
	}
	// "owners" tag: listOwners, listPetsByOwner → 2 commands.
	if len(cmds) != 2 {
		t.Errorf("got %d commands for tag=owners, want 2; keys: %v", len(cmds), cmdKeys(cmds))
	}
	if _, ok := cmds["listowners"]; !ok {
		t.Errorf("expected 'listowners' in owners-tagged results, keys: %v", cmdKeys(cmds))
	}
}

func TestSpecFilterIncludePathGlob(t *testing.T) {
	fix := petstoreFixture(t)
	svc := &Service{
		Name:    "petstore",
		BaseURL: "http://petstore.example",
		Spec:    fix,
		SpecFilter: SpecFilter{
			Include: []string{"/pets*"},
		},
	}
	cmds, err := InferredCommands(svc, filepath.Dir(fix))
	if err != nil {
		t.Fatalf("InferredCommands: %v", err)
	}
	// /pets and /pets/{petId} → 2 + 2 = 4 operations match "/pets*".
	// But /owners/{ownerId}/pets does NOT match "/pets*" (doesn't start with /pets).
	if len(cmds) != 4 {
		t.Errorf("got %d commands for path=/pets*, want 4; keys: %v", len(cmds), cmdKeys(cmds))
	}
}

func TestSpecFilterExcludeWinsOverInclude(t *testing.T) {
	fix := petstoreFixture(t)
	svc := &Service{
		Name:    "petstore",
		BaseURL: "http://petstore.example",
		Spec:    fix,
		SpecFilter: SpecFilter{
			Include: []string{"pets"},      // includes all pets-tagged ops
			Exclude: []string{"deletePet"}, // excludes by operationId
		},
	}
	cmds, err := InferredCommands(svc, filepath.Dir(fix))
	if err != nil {
		t.Fatalf("InferredCommands: %v", err)
	}
	// pets tag: listPets, createPet, getPetById, deletePet, listPetsByOwner = 5
	// exclude deletePet → 4
	if _, ok := cmds["deletepet"]; ok {
		t.Error("deletePet should be excluded but is present")
	}
	if len(cmds) != 4 {
		t.Errorf("got %d commands, want 4; keys: %v", len(cmds), cmdKeys(cmds))
	}
}

func TestSpecFilterIncludeOperationId(t *testing.T) {
	fix := petstoreFixture(t)
	svc := &Service{
		Name:    "petstore",
		BaseURL: "http://petstore.example",
		Spec:    fix,
		SpecFilter: SpecFilter{
			Include: []string{"listPets"},
		},
	}
	cmds, err := InferredCommands(svc, filepath.Dir(fix))
	if err != nil {
		t.Fatalf("InferredCommands: %v", err)
	}
	if len(cmds) != 1 {
		t.Errorf("got %d commands, want 1; keys: %v", len(cmds), cmdKeys(cmds))
	}
	if _, ok := cmds["listpets"]; !ok {
		t.Errorf("expected 'listpets', got: %v", cmdKeys(cmds))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Explicit commands override inferred

func TestExplicitCommandOverridesInferred(t *testing.T) {
	fix := petstoreFixture(t)
	svc := &Service{
		Name:    "petstore",
		BaseURL: "http://petstore.example",
		Spec:    fix,
		Commands: map[string]Command{
			// Override the inferred listpets with a custom implementation.
			"listpets": {
				Help:   "custom list",
				Method: "GET",
				Path:   "/api/v2/pets",
			},
		},
	}
	if err := mergeSpecCommands(svc, filepath.Dir(fix)); err != nil {
		t.Fatalf("mergeSpecCommands: %v", err)
	}
	// The explicit entry must win.
	if svc.Commands["listpets"].Path != "/api/v2/pets" {
		t.Errorf("explicit command overridden; path = %q, want /api/v2/pets", svc.Commands["listpets"].Path)
	}
	if svc.Commands["listpets"].Help != "custom list" {
		t.Errorf("explicit help overridden; help = %q, want 'custom list'", svc.Commands["listpets"].Help)
	}
	// All other inferred commands should still exist.
	if len(svc.Commands) != 7 {
		t.Errorf("got %d commands, want 7; keys: %v", len(svc.Commands), cmdKeys(svc.Commands))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Bad spec → validation / load error

func TestBadSpecFileError(t *testing.T) {
	svc := &Service{
		Name:    "x",
		BaseURL: "http://x.example",
		Spec:    "/nonexistent/does-not-exist.yaml",
	}
	_, err := InferredCommands(svc, "")
	if err == nil {
		t.Fatal("expected error for nonexistent spec file, got nil")
	}
	// A missing spec file is a config error (exit 2).
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("missing spec file should be a *ConfigError, got %T: %v", err, err)
	}
}

func TestInvalidYAMLSpecError(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte(":::not valid yaml:::"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		Name:    "x",
		BaseURL: "http://x.example",
		Spec:    bad,
	}
	_, err := InferredCommands(svc, dir)
	if err == nil {
		t.Fatal("expected error for invalid YAML spec, got nil")
	}
	// A readable-but-unparseable document is a decode error (exit 6).
	var decErr *DecodeError
	if !errors.As(err, &decErr) {
		t.Fatalf("unparseable spec should be a *DecodeError, got %T: %v", err, err)
	}
}

func TestSwagger2Rejected(t *testing.T) {
	swagger2 := []byte(`swagger: "2.0"
info:
  title: Old API
  version: "1.0"
paths: {}
`)
	dir := t.TempDir()
	path := filepath.Join(dir, "swagger.yaml")
	if err := os.WriteFile(path, swagger2, 0o644); err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		Name:    "x",
		BaseURL: "http://x.example",
		Spec:    path,
	}
	_, err := InferredCommands(svc, dir)
	if err == nil {
		t.Fatal("expected error for Swagger 2.0 spec, got nil")
	}
	// An unsupported spec format is a config error (exit 2).
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("swagger 2.0 should be a *ConfigError, got %T: %v", err, err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Relative path resolution

func TestRelativeSpecPath(t *testing.T) {
	// Copy the fixture to a temp dir and use a relative path from that dir.
	b, err := os.ReadFile("testdata/petstore.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "petstore.yaml"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		Name:    "petstore",
		BaseURL: "http://petstore.example",
		Spec:    "petstore.yaml", // relative path
	}
	cmds, err := InferredCommands(svc, dir)
	if err != nil {
		t.Fatalf("InferredCommands with relative path: %v", err)
	}
	if len(cmds) != 7 {
		t.Errorf("got %d commands, want 7", len(cmds))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// HTTP URL fetch

func TestInferredCommandsFromURL(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // hermetic cache, no ~/.cache pollution
	b, err := os.ReadFile("testdata/petstore.yaml")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	svc := &Service{
		Name:    "petstore",
		BaseURL: "http://petstore.example",
		Spec:    srv.URL + "/openapi.yaml",
	}
	cmds, err := InferredCommands(svc, "")
	if err != nil {
		t.Fatalf("InferredCommands from URL: %v", err)
	}
	if len(cmds) != 7 {
		t.Errorf("got %d commands from URL, want 7; keys: %v", len(cmds), cmdKeys(cmds))
	}
}

func TestFetchURLNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	svc := &Service{
		Name:    "x",
		BaseURL: "http://x.example",
		Spec:    srv.URL + "/openapi.yaml",
	}
	_, err := InferredCommands(svc, "")
	if err == nil {
		t.Fatal("expected error for HTTP 404, got nil")
	}
	// A non-200 spec fetch is a decode error (exit 6).
	var decErr *DecodeError
	if !errors.As(err, &decErr) {
		t.Fatalf("non-200 spec fetch should be a *DecodeError, got %T: %v", err, err)
	}
}

// TestFetchURLGarbageBody proves a 200 response whose body is not a parseable
// OpenAPI document classifies as a decode error (exit 6).
func TestFetchURLGarbageBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(":::not an openapi document:::"))
	}))
	defer srv.Close()

	svc := &Service{
		Name:    "x",
		BaseURL: "http://x.example",
		Spec:    srv.URL + "/openapi.yaml",
	}
	_, err := InferredCommands(svc, "")
	if err == nil {
		t.Fatal("expected error for garbage spec body, got nil")
	}
	var decErr *DecodeError
	if !errors.As(err, &decErr) {
		t.Fatalf("garbage spec body should be a *DecodeError, got %T: %v", err, err)
	}
}

// TestSpecCacheHit proves the disk cache prevents a second fetch of the same
// spec URL within the freshness window.
func TestSpecCacheHit(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	b, err := os.ReadFile("testdata/petstore.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	svc := &Service{Name: "petstore", BaseURL: "http://petstore.example", Spec: srv.URL + "/openapi.yaml"}
	if _, err := InferredCommands(svc, ""); err != nil {
		t.Fatalf("first InferredCommands: %v", err)
	}
	if _, err := InferredCommands(svc, ""); err != nil {
		t.Fatalf("second InferredCommands: %v", err)
	}
	if hits != 1 {
		t.Fatalf("server hit %d times, want 1 (second call must hit the cache)", hits)
	}
}

// TestSpecCacheRejectsNon0600 proves the cache reader trusts only the exact mode
// it writes (0600); a file at any other mode (e.g. a 0400 downgrade or a 0644
// world-readable file) is treated as externally modified and ignored.
func TestSpecCacheRejectsNon0600(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := specCachePath("http://spec.example/openapi.yaml")
	writeSpecCache(path, []byte(`{"ok":true}`))

	if _, ok := readSpecCache(path); !ok {
		t.Fatal("freshly-written 0600 cache should be a hit")
	}
	for _, mode := range []os.FileMode{0o400, 0o644} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, ok := readSpecCache(path); ok {
			t.Fatalf("cache at mode %o should be ignored, got a hit", mode)
		}
	}
}

// TestLoadDegradesOnSpecFetchFailure proves a remote spec that does not resolve
// degrades ONLY its service (kept with its static commands) and does NOT abort
// the whole load. An unrelated service must still load.
func TestLoadDegradesOnSpecFetchFailure(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	// A service whose spec: points at a closed port (immediate connection
	// refused), plus one static command.
	writeManifest(t, dir, "broken.yaml", `
name: broken
spec: http://127.0.0.1:1/openapi.yaml
commands:
  ping:
    method: GET
    path: /ping
`)
	// An unrelated, healthy service.
	writeManifest(t, dir, "healthy.yaml", `
name: healthy
commands:
  list:
    method: GET
    path: /list
`)

	l, err := Load(dir)
	if err != nil {
		t.Fatalf("Load must not abort on a per-service spec failure: %v", err)
	}
	broken, ok := l.Services["broken"]
	if !ok {
		t.Fatal("broken service should still load (degraded), but is absent")
	}
	if _, ok := broken.Commands["ping"]; !ok {
		t.Fatalf("degraded service lost its static command; commands: %v", cmdKeys(broken.Commands))
	}
	if _, ok := l.Services["healthy"]; !ok {
		t.Fatal("unrelated healthy service must still load")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Validation

func TestValidateSpecFilter(t *testing.T) {
	// spec_filter without spec is an error.
	svc := &Service{
		Name: "x",
		SpecFilter: SpecFilter{
			Include: []string{"pets"},
		},
	}
	if err := Validate(svc); err == nil {
		t.Error("expected error for spec_filter without spec, got nil")
	}
}

func TestValidateEmptyFilterPattern(t *testing.T) {
	svc := &Service{
		Name: "x",
		Spec: "openapi.yaml",
		SpecFilter: SpecFilter{
			Include: []string{""},
		},
	}
	if err := Validate(svc); err == nil {
		t.Error("expected error for empty include pattern, got nil")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Load integration: spec: in a service manifest

func TestLoadWithSpec(t *testing.T) {
	// Copy fixture into a temp config dir.
	b, err := os.ReadFile("testdata/petstore.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "petstore.yaml"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	writeManifest(t, dir, "petstore.yaml", `
name: petstore
spec: petstore.yaml
`)
	l, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	svc, ok := l.Services["petstore"]
	if !ok {
		t.Fatal("petstore service not found")
	}
	if len(svc.Commands) != 7 {
		t.Errorf("got %d commands after Load, want 7; keys: %v", len(svc.Commands), cmdKeys(svc.Commands))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// helpers

func cmdKeys(cmds map[string]Command) []string {
	keys := make([]string, 0, len(cmds))
	for k := range cmds {
		keys = append(keys, k)
	}
	return keys
}
