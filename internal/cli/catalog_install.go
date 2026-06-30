package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/spf13/cobra"
)

// A named catalog is an installable bundle of portable manifests under
// <config-dir>/catalogs/<name>/. It only makes more manifests AVAILABLE — every
// manifest is validated on add to be portable (no base_url, no secret ref), so an
// installed catalog is inert until profile.yaml binds it. Precedence at load is
// local services/ > installed catalogs > the embedded catalog. There is no
// execution-time gating here: labctl stays an unopinionated executor.

// gitURLSchemes are the transport schemes permitted for a git source URL. ext/fd
// transport helpers (which can execute arbitrary commands) are excluded by both
// this allow-list and the GIT_ALLOW_PROTOCOL env passed to git.
var gitURLSchemes = []string{"https://", "http://", "ssh://", "git://", "file://"}

// scpStyleURL matches a scp-style git remote (user@host:path).
var scpStyleURL = regexp.MustCompile(`^[A-Za-z0-9_.-]+@[A-Za-z0-9_.-]+:.+$`)

// gitRefPattern restricts --ref to a safe ref-ish token (no leading '-', no shell
// metacharacters) so it can never be read as a git option or injected.
var gitRefPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

func (r *runner) cmdCatalogAdd() *cobra.Command {
	var name, ref string
	var force bool
	cmd := &cobra.Command{
		Use:   "add <source> [--name <name>] [--ref <ref>] [--force]",
		Short: "install a named catalog of portable manifests from a dir or git URL",
		Long: "Install a named catalog of portable manifests under\n" +
			"<config-dir>/catalogs/<name>/. <source> is either an existing local directory\n" +
			"or a git URL (https/http/ssh/git/file:// or scp-style user@host:path).\n\n" +
			"Every top-level *.yaml/*.yml in the source is validated to be a PORTABLE\n" +
			"manifest — no base_url, no secret ref — before anything is written; one bad\n" +
			"manifest rejects the whole add. A git source is pinned to the resolved commit\n" +
			"SHA, so an installed catalog is a reproducible, inert bundle until profile.yaml\n" +
			"binds it.\n\n" +
			"The catalog name defaults to the dir/repo basename (a trailing .git is\n" +
			"stripped); pass --name to override, or if the inferred name is not a valid\n" +
			"single path segment. --ref selects a git branch/tag/commit. --force replaces\n" +
			"an already-installed catalog of the same name.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r.curCommand = "catalog"
			return r.catalogAdd(args[0], name, ref, force)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "catalog name (default: dir/repo basename)")
	cmd.Flags().StringVar(&ref, "ref", "", "git branch, tag, or commit to check out (git sources only)")
	cmd.Flags().BoolVar(&force, "force", false, "replace an already-installed catalog of the same name")
	return cmd
}

func (r *runner) cmdCatalogUpdate() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update [name]",
		Short: "re-fetch an installed catalog from its recorded source (all if none named)",
		Long: "Re-fetch one installed catalog (or every installed catalog when no name is\n" +
			"given) from its recorded source, re-validating each manifest as portable\n" +
			"(same fail-closed gate as `catalog add`). A git source is re-cloned at its\n" +
			"recorded ref and re-pinned to the new commit SHA. Per-catalog outcomes are\n" +
			"reported to stderr; a failure on one catalog does not abort the others.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r.curCommand = "catalog"
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return r.catalogUpdate(name)
		},
	}
	return cmd
}

func (r *runner) cmdCatalogRemove() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "uninstall a named catalog",
		Long: "Uninstall a named catalog (delete <config-dir>/catalogs/<name>/). Its\n" +
			"services disappear from the next load, re-exposing the embedded manifest of\n" +
			"the same name if there is one. Removing a catalog that is not installed is an\n" +
			"error.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r.curCommand = "catalog"
			return r.catalogRemove(args[0])
		},
	}
	return cmd
}

func (r *runner) cmdCatalogInstalled() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "installed",
		Short: "list installed named catalogs (name, type, commit/ref, source)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r.curCommand = "catalog"
			return r.catalogInstalled()
		},
	}
	return cmd
}

// catalogAdd installs a catalog from a dir or git source. Fetch → validate every
// manifest (fail-closed) → atomic install. Nothing is written unless every
// manifest is a valid portable manifest and no service name collides across
// installed catalogs.
func (r *runner) catalogAdd(source, name, ref string, force bool) error {
	srcType, err := classifySource(source)
	if err != nil {
		return err
	}
	if name == "" {
		name = inferCatalogName(source, srcType)
	}
	if err := manifest.ValidateCatalogName(name); err != nil {
		return &usageError{fmt.Sprintf("inferred catalog name %q is invalid; pass --name with a single path segment (^[a-z0-9][a-z0-9_-]*$)", name)}
	}
	if ref != "" && srcType != "git" {
		return &usageError{"--ref only applies to a git source"}
	}

	tmp, err := os.MkdirTemp("", "labctl-catalog-fetch-")
	if err != nil {
		return fmt.Errorf("creating tempdir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	now := time.Now().UTC()
	meta := manifest.CatalogMeta{Name: name, Source: source, Type: srcType, AddedAt: now, UpdatedAt: now}
	fetchDir := source
	if srcType == "git" {
		commit, err := r.gitFetch(source, ref, tmp)
		if err != nil {
			return err
		}
		fetchDir = tmp
		meta.Ref = ref
		meta.Commit = commit
	}

	files, err := r.collectAndValidate(fetchDir, source, name)
	if err != nil {
		return err
	}
	if err := manifest.InstallCatalog(r.configDir(), meta, files, force); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(r.stderr, "installed catalog %q (%s) from %s%s\n", name, countManifests(len(files)), source, commitSuffix(meta.Commit))
	return nil
}

// catalogUpdate re-fetches one or all installed catalogs. A per-catalog failure
// is reported and the first such error is returned, but it never aborts the rest.
func (r *runner) catalogUpdate(name string) error {
	configDir := r.configDir()
	var targets []string
	if name != "" {
		if err := manifest.ValidateCatalogName(name); err != nil {
			return err
		}
		targets = []string{name}
	} else {
		cats, err := manifest.InstalledCatalogs(configDir)
		if err != nil {
			return err
		}
		for _, c := range cats {
			targets = append(targets, c.Name)
		}
		if len(targets) == 0 {
			_, _ = fmt.Fprintln(r.stderr, "no catalogs installed")
			return nil
		}
	}
	var firstErr error
	for _, t := range targets {
		if err := r.updateOne(configDir, t); err != nil {
			_, _ = fmt.Fprintf(r.stderr, "catalog %q: update failed: %v\n", t, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// updateOne re-fetches and re-installs a single catalog from its recorded source.
func (r *runner) updateOne(configDir, name string) error {
	meta, found, err := manifest.ReadCatalogMeta(configDir, name)
	if err != nil {
		return err
	}
	if !found {
		return &usageError{fmt.Sprintf("catalog %q is not installed", name)}
	}
	if meta.Source == "" || meta.Type == "" {
		return fmt.Errorf("catalog %q has no recorded source; remove and re-add it", name)
	}
	// The install directory is the source of truth for the name — a hand-edited
	// lock file must not retarget the install to a different catalog.
	meta.Name = name

	tmp, err := os.MkdirTemp("", "labctl-catalog-fetch-")
	if err != nil {
		return fmt.Errorf("creating tempdir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	meta.UpdatedAt = time.Now().UTC()
	fetchDir := meta.Source
	switch meta.Type {
	case "dir":
		// re-read the source dir
	case "git":
		commit, err := r.gitFetch(meta.Source, meta.Ref, tmp)
		if err != nil {
			return err
		}
		fetchDir = tmp
		meta.Commit = commit
	default:
		return fmt.Errorf("catalog %q has unknown source type %q", name, meta.Type)
	}

	files, err := r.collectAndValidate(fetchDir, meta.Source, name)
	if err != nil {
		return err
	}
	if err := manifest.InstallCatalog(configDir, meta, files, true); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(r.stderr, "updated catalog %q (%s) from %s%s\n", name, countManifests(len(files)), meta.Source, commitSuffix(meta.Commit))
	return nil
}

func (r *runner) catalogRemove(name string) error {
	if err := manifest.RemoveCatalog(r.configDir(), name); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(r.stderr, "removed catalog %q\n", name)
	return nil
}

func (r *runner) catalogInstalled() error {
	cats, err := manifest.InstalledCatalogs(r.configDir())
	if err != nil {
		return err
	}
	for _, c := range cats {
		ver := "-"
		if c.Commit != "" {
			ver = shortSHA(c.Commit)
		} else if c.Ref != "" {
			ver = c.Ref
		}
		typ := c.Type
		if typ == "" {
			typ = "-"
		}
		src := c.Source
		if src == "" {
			src = "-"
		}
		_, _ = fmt.Fprintf(r.stdout, "%-16s %-5s %-12s %s\n", c.Name, typ, ver, src)
	}
	return nil
}

// collectAndValidate enumerates top-level *.yaml/*.yml in fetchDir, validates each
// as a portable manifest (fail-closed), rejects a duplicate service name within
// the source, and pre-checks for a service-name collision against OTHER installed
// catalogs. It returns the files keyed by base filename, ready for InstallCatalog.
func (r *runner) collectAndValidate(fetchDir, source, name string) (map[string][]byte, error) {
	entries, err := os.ReadDir(fetchDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", source, err)
	}
	files := map[string][]byte{}
	svcToFile := map[string]string{} // service name → filename, within this source
	for _, e := range entries {
		if e.IsDir() || !isYAMLFile(e.Name()) {
			continue
		}
		path := filepath.Join(fetchDir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		svcName, err := manifest.ValidatePortableManifest(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if svcName == "" {
			svcName = strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".yaml"), ".yml")
		}
		if prev, dup := svcToFile[svcName]; dup {
			return nil, &usageError{fmt.Sprintf("source defines service %q twice (%s and %s)", svcName, prev, e.Name())}
		}
		svcToFile[svcName] = e.Name()
		files[e.Name()] = b
	}
	if len(files) == 0 {
		return nil, &usageError{fmt.Sprintf("no manifests (*.yaml) found in %s", source)}
	}
	// Cross-catalog collision pre-check (exclude self so a re-add/update is fine).
	existing, err := manifest.InstalledCatalogServiceNames(r.configDir(), name)
	if err != nil {
		return nil, err
	}
	for svc := range svcToFile {
		if cat, ok := existing[svc]; ok {
			return nil, &usageError{fmt.Sprintf("service %q is already defined by installed catalog %q; remove it or rename", svc, cat)}
		}
	}
	return files, nil
}

// gitFetch clones url into tmp (optionally checking out ref) using the system git,
// and returns the resolved HEAD commit SHA. The URL and ref are validated before
// any process runs; the URL is passed as a single arg after `--` (no shell), and
// ext/fd transport helpers are blocked both by the URL validation and by
// GIT_ALLOW_PROTOCOL in the subprocess env.
func (r *runner) gitFetch(url, ref, tmp string) (string, error) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return "", &usageError{"git is required to add a git catalog source but was not found in PATH"}
	}
	if err := validateGitURL(url); err != nil {
		return "", err
	}
	if ref != "" {
		if err := validateGitRef(ref); err != nil {
			return "", err
		}
	}
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:http:ssh:git:file")
	run := func(args ...string) ([]byte, error) {
		c := exec.Command(gitBin, args...) // #nosec G204 -- argv built from a validated URL/ref, passed after --, no shell
		c.Env = env
		c.Stdin = nil
		var stderr bytes.Buffer
		c.Stderr = &stderr
		out, err := c.Output()
		if err != nil {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	if _, err := run("-c", "protocol.ext.allow=never", "-c", "protocol.fd.allow=never", "clone", "--quiet", "--", url, tmp); err != nil {
		return "", err
	}
	if ref != "" {
		// `--` separates the ref from any pathspec so a ref can never be read as an
		// option, matching the clone call above (ref is also pre-validated).
		if _, err := run("-C", tmp, "checkout", "--quiet", ref, "--"); err != nil {
			return "", err
		}
	}
	out, err := run("-C", tmp, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// classifySource decides whether source is a local dir or a git URL. An existing
// non-dir path, or a path that is neither an existing dir nor a plausible git URL,
// is a usage error.
func classifySource(source string) (string, error) {
	if source == "" {
		return "", &usageError{"empty catalog source"}
	}
	if info, err := os.Stat(source); err == nil {
		if !info.IsDir() {
			return "", &usageError{fmt.Sprintf("source %q is a file, not a directory or git URL", source)}
		}
		return "dir", nil
	}
	if err := validateGitURL(source); err != nil {
		return "", err
	}
	return "git", nil
}

// inferCatalogName derives a catalog name from the source basename, stripping a
// trailing .git for git URLs. The result is validated by the caller.
func inferCatalogName(source, srcType string) string {
	if srcType == "dir" {
		return filepath.Base(filepath.Clean(source))
	}
	base := source
	if i := strings.LastIndexAny(base, "/:"); i >= 0 {
		base = base[i+1:]
	}
	return strings.TrimSuffix(base, ".git")
}

// validateGitURL allows only the safe transport schemes and scp-style remotes,
// and rejects anything that could invoke a transport helper (scheme::) or be read
// as a git option (leading '-').
func validateGitURL(url string) error {
	if url == "" {
		return &usageError{"empty git URL"}
	}
	if strings.HasPrefix(url, "-") {
		return &usageError{fmt.Sprintf("invalid git URL %q: must not start with '-'", url)}
	}
	// Reject anything containing "::" — git reads scheme::path as a transport
	// helper (ext::/fd:: can run arbitrary commands). This also rejects a bare IPv6
	// literal, but those aren't valid git remotes without a scheme anyway, so the
	// over-broad guard is the safe tradeoff here.
	if strings.Contains(url, "::") {
		return &usageError{fmt.Sprintf("invalid git URL %q: transport helpers (scheme::) are not allowed", url)}
	}
	for _, scheme := range gitURLSchemes {
		if strings.HasPrefix(url, scheme) {
			return nil
		}
	}
	if scpStyleURL.MatchString(url) {
		return nil
	}
	return &usageError{fmt.Sprintf("source %q is neither an existing directory nor a valid git URL (want https/http/ssh/git/file:// or user@host:path)", url)}
}

// validateGitRef restricts --ref to a safe ref-ish token.
func validateGitRef(ref string) error {
	if strings.HasPrefix(ref, "-") {
		return &usageError{fmt.Sprintf("invalid --ref %q: must not start with '-'", ref)}
	}
	if !gitRefPattern.MatchString(ref) {
		return &usageError{fmt.Sprintf("invalid --ref %q: must match ^[A-Za-z0-9._/-]+$", ref)}
	}
	return nil
}

// commitSuffix renders a "@<short-sha>" suffix for confirmation lines, or "" when
// there is no commit (a dir source).
func commitSuffix(commit string) string {
	if commit == "" {
		return ""
	}
	return "@" + shortSHA(commit)
}

// shortSHA truncates a git SHA to 12 chars for display.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// isYAMLFile reports whether name has a .yaml/.yml extension.
func isYAMLFile(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

// countManifests renders a correctly-pluralized "N manifest(s)" phrase.
func countManifests(n int) string {
	if n == 1 {
		return "1 manifest"
	}
	return fmt.Sprintf("%d manifests", n)
}
