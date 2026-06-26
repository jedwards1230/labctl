package secret

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/jedwards1230/labctl/internal/manifest"
)

func resolverSpec() manifest.SecretResolver {
	return manifest.SecretResolver{Command: []string{"op", "read", "{ref}"}, EnvOverride: true}
}

func TestResolveRead(t *testing.T) {
	var gotArgv []string
	run := func(argv []string) (string, error) {
		gotArgv = argv
		return "resolved-value", nil
	}
	r := New(resolverSpec(),
		map[string]manifest.Secret{"api_key": {Ref: "op://homelab/Radarr/api_key"}},
		"RADARR", run)
	r.withGetenv(func(string) string { return "" })

	v, err := r.Secret("api_key")
	if err != nil {
		t.Fatal(err)
	}
	if v != "resolved-value" {
		t.Fatalf("got %q", v)
	}
	if strings.Join(gotArgv, " ") != "op read op://homelab/Radarr/api_key" {
		t.Fatalf("argv = %v", gotArgv)
	}
}

func TestResolveCaches(t *testing.T) {
	calls := 0
	run := func(argv []string) (string, error) { calls++; return "v", nil }
	r := New(resolverSpec(), map[string]manifest.Secret{"k": {Ref: "op://a/b/c"}}, "", run)
	r.withGetenv(func(string) string { return "" })
	for i := 0; i < 3; i++ {
		if _, err := r.Secret("k"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("resolver called %d times, want 1 (cached)", calls)
	}
}

func TestEnvOverridePrecedence(t *testing.T) {
	run := func(argv []string) (string, error) { return "from-op", nil }
	// Explicit per-secret env wins over the resolver.
	r := New(resolverSpec(), map[string]manifest.Secret{"api_key": {Ref: "op://a/b/c", Env: "RADARR_API_KEY"}}, "RADARR", run)
	r.withGetenv(func(k string) string {
		if k == "RADARR_API_KEY" {
			return "from-env"
		}
		return ""
	})
	v, err := r.Secret("api_key")
	if err != nil {
		t.Fatal(err)
	}
	if v != "from-env" {
		t.Fatalf("got %q, want from-env (env should win)", v)
	}
}

func TestEnvPrefixOverride(t *testing.T) {
	run := func(argv []string) (string, error) { return "from-op", nil }
	r := New(resolverSpec(), map[string]manifest.Secret{"api_key": {Ref: "op://a/b/c"}}, "RADARR", run)
	r.withGetenv(func(k string) string {
		if k == "RADARR_API_KEY" {
			return "prefixed"
		}
		return ""
	})
	v, _ := r.Secret("api_key")
	if v != "prefixed" {
		t.Fatalf("got %q, want prefixed (<PREFIX>_<NAME> override)", v)
	}
}

func TestFieldFallback(t *testing.T) {
	// First field empty, second returns a value.
	run := func(argv []string) (string, error) {
		ref := argv[len(argv)-1]
		if strings.HasSuffix(ref, "/credential") {
			return "", nil
		}
		if strings.HasSuffix(ref, "/password") {
			return "pw", nil
		}
		return "", nil
	}
	r := New(resolverSpec(),
		map[string]manifest.Secret{"k": {Ref: "op://a/n8n/credential", Fields: []string{"credential", "password"}}},
		"", run)
	r.withGetenv(func(string) string { return "" })
	v, err := r.Secret("k")
	if err != nil {
		t.Fatal(err)
	}
	if v != "pw" {
		t.Fatalf("got %q, want pw (field fallback)", v)
	}
}

func TestUndeclaredSecret(t *testing.T) {
	r := New(resolverSpec(), map[string]manifest.Secret{}, "", func([]string) (string, error) { return "", nil })
	if _, err := r.Secret("nope"); err == nil {
		t.Fatal("expected error for undeclared secret")
	}
}

func TestBuildResolverEnv_FileSource(t *testing.T) {
	readFile := func(path string) ([]byte, error) {
		if path != "/etc/sa-token" {
			t.Fatalf("unexpected path %q", path)
		}
		return []byte("tok\n"), nil // trailing whitespace must be trimmed
	}
	spec := map[string]manifest.SecretEnvSource{
		"OP_SERVICE_ACCOUNT_TOKEN": {File: "/etc/sa-token"},
	}
	out, err := buildResolverEnv(spec, func(string) string { return "" }, readFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0] != "OP_SERVICE_ACCOUNT_TOKEN=tok" {
		t.Fatalf("got %v, want [OP_SERVICE_ACCOUNT_TOKEN=tok]", out)
	}
}

func TestBuildResolverEnv_ValueSource(t *testing.T) {
	spec := map[string]manifest.SecretEnvSource{"FOO": {Value: "bar"}}
	out, err := buildResolverEnv(spec, func(string) string { return "" }, failingReadFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0] != "FOO=bar" {
		t.Fatalf("got %v, want [FOO=bar]", out)
	}
}

func TestBuildResolverEnv_EnvSource(t *testing.T) {
	getenv := func(k string) string {
		if k == "SRC_VAR" {
			return "from-env"
		}
		return ""
	}
	spec := map[string]manifest.SecretEnvSource{"DEST": {Env: "SRC_VAR"}}
	out, err := buildResolverEnv(spec, getenv, failingReadFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0] != "DEST=from-env" {
		t.Fatalf("got %v, want [DEST=from-env]", out)
	}
}

func TestBuildResolverEnv_FileMissing(t *testing.T) {
	readFile := func(string) ([]byte, error) { return nil, fmt.Errorf("no such file") }
	spec := map[string]manifest.SecretEnvSource{"OP_SERVICE_ACCOUNT_TOKEN": {File: "/nope"}}
	out, err := buildResolverEnv(spec, func(string) string { return "" }, readFile)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "OP_SERVICE_ACCOUNT_TOKEN") {
		t.Fatalf("error %q should name the var", err)
	}
	if out != nil {
		t.Fatalf("expected nil output on error, got %v", out)
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	getenv := func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"tilde-slash", "~/x", home + "/x"},
		{"home-var", "$HOME/x", home + "/x"},
		{"absolute-untouched", "/etc/token", "/etc/token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := expandPath(tc.in, getenv); got != tc.want {
				t.Fatalf("expandPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExecRunnerEnvInjected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh-based subprocess test is POSIX-only")
	}
	env := append(os.Environ(), "INJECT=secret-value")
	out, err := execRunnerEnv([]string{"/bin/sh", "-c", `printf %s "$INJECT"`}, env)
	if err != nil {
		t.Fatal(err)
	}
	if out != "secret-value" {
		t.Fatalf("subprocess saw INJECT=%q, want secret-value", out)
	}
	// The replaced env must still carry PATH (os.Environ() was the base).
	path, err := execRunnerEnv([]string{"/bin/sh", "-c", `printf %s "$PATH"`}, env)
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("subprocess lost PATH — env was replaced without os.Environ() base")
	}
}

// failingReadFile returns a readFile that fails the test if called — used to
// prove non-file sources never touch the filesystem.
func failingReadFile(t *testing.T) func(string) ([]byte, error) {
	t.Helper()
	return func(path string) ([]byte, error) {
		t.Fatalf("readFile must not be called for a non-file source (got %q)", path)
		return nil, nil
	}
}
