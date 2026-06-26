package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeGH is a hermetic stand-in for the GitHub releases API + asset CDN. It
// serves a single release and its asset (+ .sha256 sibling) entirely in memory.
type fakeGH struct {
	srv        *httptest.Server
	goos       string
	goarch     string
	tag        string
	assetBytes []byte
	shaContent string // full .sha256 body; "" → computed from assetBytes
	omitAsset  bool
	omitSha    bool
	apiStatus  int    // non-zero, ≥400 → release endpoint returns this status
	wantToken  string // if set, release endpoint 401s without a matching Bearer

	downloads atomic.Int64 // count of asset-byte fetches (proves no-download paths)
	lastPath  atomic.Value // string: last release path served
}

func (f *fakeGH) assetName() string { return fmt.Sprintf("labctl-%s-%s", f.goos, f.goarch) }

// newFakeGH wires the httptest server. Release JSON points asset URLs back at
// this same server so downloads stay loopback (scheme matches the http apiBase).
func newFakeGH(t *testing.T, f *fakeGH) *fakeGH {
	t.Helper()
	mux := http.NewServeMux()

	releaseJSON := func() []byte {
		rel := ghRelease{TagName: f.tag}
		if !f.omitAsset {
			rel.Assets = append(rel.Assets, ghAsset{
				Name: f.assetName(),
				URL:  f.srv.URL + "/dl/" + f.assetName(),
			})
		}
		if !f.omitSha {
			rel.Assets = append(rel.Assets, ghAsset{
				Name: f.assetName() + ".sha256",
				URL:  f.srv.URL + "/dl/" + f.assetName() + ".sha256",
			})
		}
		b, _ := json.Marshal(rel)
		return b
	}

	serveRelease := func(w http.ResponseWriter, r *http.Request) {
		f.lastPath.Store(r.URL.Path)
		if f.wantToken != "" && r.Header.Get("Authorization") != "Bearer "+f.wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		if f.apiStatus >= 400 {
			w.WriteHeader(f.apiStatus)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
			return
		}
		_, _ = w.Write(releaseJSON())
	}

	mux.HandleFunc("/repos/jedwards1230/labctl/releases/latest", serveRelease)
	mux.HandleFunc("/repos/jedwards1230/labctl/releases/tags/", serveRelease)

	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".sha256"):
			content := f.shaContent
			if content == "" {
				sum := sha256.Sum256(f.assetBytes)
				content = hex.EncodeToString(sum[:]) + "  " + f.assetName()
			}
			_, _ = w.Write([]byte(content))
		default:
			f.downloads.Add(1)
			_, _ = w.Write(f.assetBytes)
		}
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// updaterFor builds a hermetic updater pointed at the fake server, writing into
// a temp file that stands in for the installed binary.
func updaterFor(t *testing.T, f *fakeGH, current, exePath string) (*selfUpdater, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errb bytes.Buffer
	u := &selfUpdater{
		apiBase:    f.srv.URL,
		httpClient: f.srv.Client(),
		goos:       f.goos,
		goarch:     f.goarch,
		current:    current,
		exePath:    func() (string, error) { return exePath, nil },
		stdout:     &out,
		stderr:     &errb,
	}
	return u, &out, &errb
}

// makeExe writes a temp file standing in for the installed binary.
func makeExe(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "labctl")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSelfUpdateHappyPath(t *testing.T) {
	f := newFakeGH(t, &fakeGH{goos: "linux", goarch: "amd64", tag: "v0.5.0", assetBytes: []byte("NEW-BINARY-BYTES")})
	exe := makeExe(t, "OLD-BINARY")
	u, out, _ := updaterFor(t, f, "v0.4.0", exe)

	if err := u.run(selfUpdateOpts{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW-BINARY-BYTES" {
		t.Fatalf("binary not swapped: %q", got)
	}
	if !strings.Contains(out.String(), "updated v0.4.0 → v0.5.0") {
		t.Fatalf("stdout = %q, want 'updated v0.4.0 → v0.5.0'", out.String())
	}
}

func TestSelfUpdateInstalledFromDev(t *testing.T) {
	f := newFakeGH(t, &fakeGH{goos: "darwin", goarch: "arm64", tag: "v0.5.0", assetBytes: []byte("BIN")})
	exe := makeExe(t, "OLD")
	u, out, _ := updaterFor(t, f, "dev", exe)

	if err := u.run(selfUpdateOpts{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "installed v0.5.0") {
		t.Fatalf("stdout = %q, want 'installed v0.5.0'", out.String())
	}
}

func TestSelfUpdateCheckNoDownload(t *testing.T) {
	f := newFakeGH(t, &fakeGH{goos: "linux", goarch: "amd64", tag: "v0.5.0", assetBytes: []byte("BIN")})
	exe := makeExe(t, "OLD")
	u, out, _ := updaterFor(t, f, "v0.4.0", exe)

	if err := u.run(selfUpdateOpts{check: true}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "current v0.4.0, latest v0.5.0") || !strings.Contains(out.String(), "update available") {
		t.Fatalf("stdout = %q, want current/latest + 'update available'", out.String())
	}
	if f.downloads.Load() != 0 {
		t.Fatalf("--check downloaded an asset (%d hits)", f.downloads.Load())
	}
	if b, _ := os.ReadFile(exe); string(b) != "OLD" {
		t.Fatalf("--check modified the binary: %q", b)
	}
}

func TestSelfUpdateAlreadyUpToDate(t *testing.T) {
	f := newFakeGH(t, &fakeGH{goos: "linux", goarch: "amd64", tag: "v0.5.0", assetBytes: []byte("BIN")})
	exe := makeExe(t, "OLD")
	u, out, _ := updaterFor(t, f, "v0.5.0", exe)

	if err := u.run(selfUpdateOpts{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "already up to date (v0.5.0)") {
		t.Fatalf("stdout = %q, want 'already up to date'", out.String())
	}
	if f.downloads.Load() != 0 {
		t.Fatalf("up-to-date path downloaded an asset (%d hits)", f.downloads.Load())
	}
	if b, _ := os.ReadFile(exe); string(b) != "OLD" {
		t.Fatalf("up-to-date path modified the binary: %q", b)
	}
}

func TestSelfUpdateForceOnEqualTag(t *testing.T) {
	f := newFakeGH(t, &fakeGH{goos: "linux", goarch: "amd64", tag: "v0.5.0", assetBytes: []byte("FORCED")})
	exe := makeExe(t, "OLD")
	u, _, _ := updaterFor(t, f, "v0.5.0", exe)

	if err := u.run(selfUpdateOpts{force: true}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "FORCED" {
		t.Fatalf("--force did not reinstall: %q", b)
	}
	if f.downloads.Load() != 1 {
		t.Fatalf("--force download count = %d, want 1", f.downloads.Load())
	}
}

func TestSelfUpdateSHAMismatchAborts(t *testing.T) {
	bad := strings.Repeat("0", 64) + "  labctl-linux-amd64"
	f := newFakeGH(t, &fakeGH{goos: "linux", goarch: "amd64", tag: "v0.5.0", assetBytes: []byte("NEW"), shaContent: bad})
	exe := makeExe(t, "ORIGINAL")
	u, _, _ := updaterFor(t, f, "v0.4.0", exe)

	err := u.run(selfUpdateOpts{})
	if err == nil {
		t.Fatal("expected sha mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("error = %v, want sha256 mismatch", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "ORIGINAL" {
		t.Fatalf("sha mismatch left the binary modified: %q", b)
	}
	if got := classify(err); got != exitGeneral {
		t.Fatalf("classify = %d, want %d (general)", got, exitGeneral)
	}
}

func TestSelfUpdateMissingAssetIsUsageError(t *testing.T) {
	// Release exists but carries no asset for this platform.
	f := newFakeGH(t, &fakeGH{goos: "plan9", goarch: "mips", tag: "v0.5.0", omitAsset: true, omitSha: true})
	exe := makeExe(t, "OLD")
	u, _, _ := updaterFor(t, f, "v0.4.0", exe)

	err := u.run(selfUpdateOpts{})
	if err == nil {
		t.Fatal("expected usage error for missing asset, got nil")
	}
	if got := classify(err); got != exitUsage {
		t.Fatalf("classify = %d, want %d (usage)", got, exitUsage)
	}
	if !strings.Contains(err.Error(), "plan9/mips") {
		t.Fatalf("error = %v, want it to name the platform", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "OLD" {
		t.Fatalf("missing-asset path modified the binary: %q", b)
	}
}

func TestSelfUpdateMissingSHA256IsUsageError(t *testing.T) {
	// Binary asset is present but its .sha256 sidecar is missing — same
	// non-transient platform/packaging class as a missing binary (exit 2).
	f := newFakeGH(t, &fakeGH{goos: "linux", goarch: "amd64", tag: "v0.5.0", assetBytes: []byte("NEW"), omitSha: true})
	exe := makeExe(t, "OLD")
	u, _, _ := updaterFor(t, f, "v0.4.0", exe)

	err := u.run(selfUpdateOpts{})
	if err == nil {
		t.Fatal("expected usage error for missing .sha256 sibling, got nil")
	}
	if got := classify(err); got != exitUsage {
		t.Fatalf("classify = %d, want %d (usage)", got, exitUsage)
	}
	if !strings.Contains(err.Error(), ".sha256") {
		t.Fatalf("error = %v, want it to mention the missing .sha256", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "OLD" {
		t.Fatalf("missing-sha path modified the binary: %q", b)
	}
}

func TestSelfUpdatePinnedVersionHitsTagsPath(t *testing.T) {
	f := newFakeGH(t, &fakeGH{goos: "linux", goarch: "amd64", tag: "v0.3.0", assetBytes: []byte("PINNED")})
	exe := makeExe(t, "OLD")
	u, _, _ := updaterFor(t, f, "v0.2.0", exe)

	if err := u.run(selfUpdateOpts{version: "v0.3.0"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if p, _ := f.lastPath.Load().(string); !strings.Contains(p, "/releases/tags/v0.3.0") {
		t.Fatalf("release path = %q, want the tags endpoint", p)
	}
	if b, _ := os.ReadFile(exe); string(b) != "PINNED" {
		t.Fatalf("pinned version did not install: %q", b)
	}
}

func TestSelfUpdateAPINon200(t *testing.T) {
	f := newFakeGH(t, &fakeGH{goos: "linux", goarch: "amd64", tag: "v9.9.9", apiStatus: http.StatusNotFound})
	exe := makeExe(t, "OLD")
	u, _, _ := updaterFor(t, f, "v0.4.0", exe)

	err := u.run(selfUpdateOpts{version: "v9.9.9"})
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	if got := classify(err); got != exitHTTP {
		t.Fatalf("classify = %d, want %d (http)", got, exitHTTP)
	}
	if b, _ := os.ReadFile(exe); string(b) != "OLD" {
		t.Fatalf("API-404 path modified the binary: %q", b)
	}
}

func TestSelfUpdateSendsBearerToken(t *testing.T) {
	f := newFakeGH(t, &fakeGH{goos: "linux", goarch: "amd64", tag: "v0.5.0", assetBytes: []byte("BIN"), wantToken: "secret-tok"})
	exe := makeExe(t, "OLD")
	u, _, _ := updaterFor(t, f, "v0.4.0", exe)
	u.token = "secret-tok"

	if err := u.run(selfUpdateOpts{check: true}); err != nil {
		t.Fatalf("run with token: %v", err)
	}

	// Without the token the server 401s → an HTTP error (exit 4).
	u.token = ""
	err := u.run(selfUpdateOpts{check: true})
	if err == nil {
		t.Fatal("expected 401 without token, got nil")
	}
	if got := classify(err); got != exitHTTP {
		t.Fatalf("classify = %d, want %d (http) for 401", got, exitHTTP)
	}
}

func TestGithubTokenPrecedence(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "primary")
	t.Setenv("GH_TOKEN", "fallback")
	if got := githubToken(); got != "primary" {
		t.Fatalf("githubToken = %q, want GITHUB_TOKEN to win", got)
	}
	t.Setenv("GITHUB_TOKEN", "")
	if got := githubToken(); got != "fallback" {
		t.Fatalf("githubToken = %q, want GH_TOKEN fallback", got)
	}
}

func TestParseSHA256(t *testing.T) {
	good := hex.EncodeToString(func() []byte { s := sha256.Sum256([]byte("x")); return s[:] }())
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"sha256sum format", good + "  labctl-linux-amd64", good, false},
		{"digest only", good, good, false},
		{"empty", "   ", "", true},
		{"short", "abc123  file", "", true},
		{"non-hex", strings.Repeat("z", 64) + "  file", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseSHA256([]byte(c.in))
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseSHA256(%q) = %q, want error", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSHA256(%q): %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("parseSHA256(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSelfUpdateSchemeDowngradeRejected(t *testing.T) {
	// apiBase https but an asset URL on http → refused before any fetch.
	u := &selfUpdater{apiBase: "https://api.github.com"}
	if err := u.checkScheme("http://evil.example/labctl-linux-amd64"); err == nil {
		t.Fatal("expected scheme-downgrade rejection, got nil")
	}
	if err := u.checkScheme("https://objects.githubusercontent.com/x"); err != nil {
		t.Fatalf("https asset should pass: %v", err)
	}
}

// TestSelfUpdateRegistered confirms the builtin is wired into the command tree
// and its help renders without touching the network (cobra handles --help).
func TestSelfUpdateRegistered(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := Run([]string{"self-update", "--help"}, &out, &errb); code != exitOK {
		t.Fatalf("self-update --help exit = %d (stderr: %s)", code, errb.String())
	}
	combined := out.String() + errb.String()
	if !strings.Contains(combined, "self-update") || !strings.Contains(combined, "--check") {
		t.Fatalf("help = %q, want it to describe self-update + --check", combined)
	}
}
