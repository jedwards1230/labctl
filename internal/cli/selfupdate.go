package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jedwards1230/labctl/internal/transport"
	"github.com/spf13/cobra"
)

// selfUpdateRepo is the GitHub owner/repo self-update pulls release assets from.
const selfUpdateRepo = "jedwards1230/labctl"

// selfUpdateOpts holds the parsed flags for `labctl self-update`.
type selfUpdateOpts struct {
	check   bool
	version string // pinned tag (vX.Y.Z); empty → latest
	force   bool
}

// selfUpdater resolves a labctl GitHub release for the running platform,
// verifies the asset's sha256, and atomically replaces the running binary.
// Every field is injectable so tests are fully hermetic (httptest apiBase, a
// temp file as exePath) — it never has to touch the real GitHub API or the real
// installed binary. This is a CLI utility command, orthogonal to engine.Execute;
// it gates nothing on the service-execution path.
type selfUpdater struct {
	apiBase    string       // GitHub API base, e.g. "https://api.github.com"
	httpClient *http.Client // bounded-timeout client
	goos       string       // runtime.GOOS by default
	goarch     string       // runtime.GOARCH by default
	current    string       // cli.Version
	exePath    func() (string, error)
	token      string // GITHUB_TOKEN / GH_TOKEN, optional
	stdout     io.Writer
	stderr     io.Writer
}

// ghAsset / ghRelease model the slice of the GitHub releases API we consume.
type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

// newSelfUpdater wires the real defaults around the runner's writers.
func (r *runner) newSelfUpdater() *selfUpdater {
	return &selfUpdater{
		apiBase:    "https://api.github.com",
		httpClient: &http.Client{Timeout: 60 * time.Second},
		goos:       runtime.GOOS,
		goarch:     runtime.GOARCH,
		current:    Version,
		exePath:    os.Executable,
		token:      githubToken(),
		stdout:     r.stdout,
		stderr:     r.stderr,
	}
}

// githubToken prefers GITHUB_TOKEN, then GH_TOKEN; empty is fine for a public repo.
func githubToken() string {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	return os.Getenv("GH_TOKEN")
}

func (r *runner) cmdSelfUpdate() *cobra.Command {
	var opts selfUpdateOpts
	cmd := &cobra.Command{
		Use:   "self-update",
		Short: "update labctl to the latest GitHub release",
		Long: "Download the matching GitHub release binary (labctl-{os}-{arch}), verify\n" +
			"its sha256, and atomically replace the running binary in place. Use --check\n" +
			"to compare versions without downloading. This is a CLI utility — it gates\n" +
			"nothing on the service-execution path and never escalates privileges.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r.curCommand = "self-update"
			return r.newSelfUpdater().run(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.check, "check", false, "report current vs latest without downloading")
	cmd.Flags().StringVar(&opts.version, "version", "", "pin a release tag (vX.Y.Z) instead of latest")
	cmd.Flags().BoolVar(&opts.force, "force", false, "reinstall even when already up to date")
	return cmd
}

// run executes the resolve → (check | verify → replace) flow.
func (u *selfUpdater) run(opts selfUpdateOpts) error {
	rel, err := u.resolveRelease(opts.version)
	if err != nil {
		return err
	}
	tag := rel.TagName
	devCurrent := u.current == "dev"

	if opts.check {
		u.reportCheck(tag, devCurrent)
		return nil
	}

	// A real, equal tag with no --force is a no-op. "dev" can't be compared, so
	// we always proceed there.
	if !devCurrent && tag == u.current && !opts.force {
		_, _ = fmt.Fprintf(u.stdout, "already up to date (%s)\n", tag)
		return nil
	}

	assetName := fmt.Sprintf("labctl-%s-%s", u.goos, u.goarch)
	asset, ok := findAsset(rel.Assets, assetName)
	if !ok {
		// No binary for this platform is an environment problem (exit 2).
		return &usageError{fmt.Sprintf("release %s has no asset %q for this platform (%s/%s)", tag, assetName, u.goos, u.goarch)}
	}
	shaAsset, ok := findAsset(rel.Assets, assetName+".sha256")
	if !ok {
		// A release lacking the .sha256 sidecar can't be installed here — same
		// non-transient platform/packaging class as a missing binary (exit 2).
		return &usageError{fmt.Sprintf("release %s asset %q has no .sha256 sibling to verify against", tag, assetName)}
	}

	_, _ = fmt.Fprintf(u.stderr, "resolving %s...\ndownloading %s...\n", tag, assetName)
	binBytes, err := u.download(asset.URL)
	if err != nil {
		return err
	}
	shaBytes, err := u.download(shaAsset.URL)
	if err != nil {
		return err
	}

	// Verify BEFORE touching the target — never write an unverified payload.
	want, err := parseSHA256(shaBytes)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(u.stderr, "verifying sha256...")
	sum := sha256.Sum256(binBytes)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("sha256 mismatch for %s: got %s, want %s (aborting; nothing written)", assetName, got, want)
	}

	exe, err := u.resolveExe()
	if err != nil {
		return err
	}
	if err := replaceBinary(exe, binBytes); err != nil {
		return err
	}

	if devCurrent {
		_, _ = fmt.Fprintf(u.stdout, "installed %s\n", tag)
	} else {
		_, _ = fmt.Fprintf(u.stdout, "updated %s → %s\n", u.current, tag)
	}
	return nil
}

// reportCheck prints the current/latest comparison for --check (stdout, no I/O).
func (u *selfUpdater) reportCheck(tag string, devCurrent bool) {
	switch {
	case devCurrent:
		_, _ = fmt.Fprintf(u.stdout, "current dev, latest %s (run without --check to install)\n", tag)
	case tag == u.current:
		_, _ = fmt.Fprintf(u.stdout, "current %s, latest %s (up to date)\n", u.current, tag)
	default:
		_, _ = fmt.Fprintf(u.stdout, "current %s, latest %s (update available)\n", u.current, tag)
	}
}

// resolveRelease fetches the latest release, or the pinned tag when version is set.
func (u *selfUpdater) resolveRelease(version string) (*ghRelease, error) {
	var endpoint string
	if version == "" {
		endpoint = fmt.Sprintf("%s/repos/%s/releases/latest", u.apiBase, selfUpdateRepo)
	} else {
		endpoint = fmt.Sprintf("%s/repos/%s/releases/tags/%s", u.apiBase, selfUpdateRepo, version)
	}
	body, err := u.get(endpoint, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	var rel ghRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, &decodeError{fmt.Errorf("decoding release JSON: %w", err)}
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("release response carried no tag_name")
	}
	return &rel, nil
}

// download fetches an asset's bytes.
func (u *selfUpdater) download(assetURL string) ([]byte, error) {
	return u.get(assetURL, "application/octet-stream")
}

// get performs a GET with the optional Bearer token, mapping a ≥400 status to a
// transport.HTTPError (exit 4) and a dial/IO failure to a transport.NetworkError
// (exit 5). It refuses any URL whose scheme does not match the configured
// apiBase scheme — in production apiBase is https, so this rejects a release
// JSON that tries to downgrade an asset URL to http; in tests apiBase is the
// httptest http origin, so http assets are allowed.
func (u *selfUpdater) get(endpoint, accept string) ([]byte, error) {
	if err := u.checkScheme(endpoint); err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if u.token != "" {
		req.Header.Set("Authorization", "Bearer "+u.token)
	}
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return nil, &transport.NetworkError{Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &transport.NetworkError{Err: err}
	}
	if resp.StatusCode >= 400 {
		return nil, &transport.HTTPError{
			Status: resp.StatusCode,
			Method: http.MethodGet,
			URL:    endpoint,
			Detail: extractGHMessage(body),
		}
	}
	return body, nil
}

// checkScheme rejects a URL whose scheme differs from apiBase's (downgrade guard).
func (u *selfUpdater) checkScheme(rawURL string) error {
	base, err := url.Parse(u.apiBase)
	if err != nil {
		return fmt.Errorf("invalid apiBase %q: %w", u.apiBase, err)
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	if target.Scheme != base.Scheme {
		return fmt.Errorf("refusing %q: scheme %q does not match %q", rawURL, target.Scheme, base.Scheme)
	}
	return nil
}

// resolveExe returns the real path of the running binary, resolving symlinks so
// the rename lands on the actual file.
func (u *selfUpdater) resolveExe() (string, error) {
	exe, err := u.exePath()
	if err != nil {
		return "", fmt.Errorf("resolving current executable path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// replaceBinary writes data to a unique temp file in exe's directory, chmods it
// 0755, then renames it over exe (same-filesystem, atomic). On any failure it
// removes the temp file, leaving the target untouched — never a partial binary.
func replaceBinary(exe string, data []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".labctl-*.new")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w (re-run with write access to that directory; labctl does not escalate privileges)", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("writing new binary to %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("closing new binary %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		cleanup()
		return fmt.Errorf("chmod new binary %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, exe); err != nil {
		cleanup()
		return fmt.Errorf("replacing %s: %w (re-run with write access to that path; labctl does not escalate privileges)", exe, err)
	}
	return nil
}

// findAsset returns the named release asset.
func findAsset(assets []ghAsset, name string) (ghAsset, bool) {
	for _, a := range assets {
		if a.Name == name {
			return a, true
		}
	}
	return ghAsset{}, false
}

// parseSHA256 reads the hex digest from sha256sum-format content ("<hex>␠␠<file>").
func parseSHA256(b []byte) (string, error) {
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return "", fmt.Errorf("empty .sha256 file")
	}
	digest := fields[0]
	if len(digest) != 64 {
		return "", fmt.Errorf("malformed sha256 digest %q (want 64 hex chars)", digest)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", fmt.Errorf("malformed sha256 digest %q: %w", digest, err)
	}
	return digest, nil
}

// extractGHMessage pulls the {"message": "..."} field from a GitHub error body.
func extractGHMessage(body []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &m); err == nil {
		return m.Message
	}
	return ""
}
