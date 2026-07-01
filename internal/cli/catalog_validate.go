package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jedwards1230/labctl/internal/agentsafety"
	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/spf13/cobra"
)

// cmdCatalogValidate is a read-only check for a community catalog repository:
// it validates every top-level manifest in a directory against the exact gate
// `catalog add` enforces, with no network access, no config dir, and no
// install/profile/cross-catalog interaction. It is what a third-party catalog
// repo (and the validate-catalog GitHub Action) runs in CI to confirm its
// manifests satisfy labctl's portable-manifest contract before anyone installs
// them with `labctl catalog add`.
func (r *runner) cmdCatalogValidate() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate <dir>",
		Short: "validate every manifest in a directory as a portable catalog manifest (read-only)",
		Long: "Validate every top-level *.yaml/*.yml in <dir> as a PORTABLE manifest — the\n" +
			"same fail-closed gate `catalog add` runs (JSON Schema, then structural\n" +
			"Validate, which rejects an in-manifest base_url or secret ref). A duplicate\n" +
			"service name across two manifests in the directory is also rejected.\n\n" +
			"This command is read-only: no network call, no install, no profile binding,\n" +
			"and no interaction with any installed or embedded catalog — it only inspects\n" +
			"the files in <dir>. That makes it the check a third-party catalog repository\n" +
			"runs in its own CI (see .github/actions/validate-catalog) to confirm its\n" +
			"manifests satisfy labctl's contract before anyone runs `labctl catalog add`\n" +
			"against the repo.\n\n" +
			"Prints one line per manifest (\"ok\" or \"FAIL\" with the reason) and exits 0\n" +
			"only if every manifest is valid and at least one was found.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r.curCommand = "catalog"
			return r.catalogValidate(args[0])
		},
	}
	return cmd
}

// catalogValidateResult is one manifest file's validation outcome.
type catalogValidateResult struct {
	file string
	name string
	err  error
}

// catalogValidate validates every top-level *.yaml/*.yml in dir, printing a
// per-file result to stdout. It shares the read → ValidatePortableManifest →
// stem-fallback → duplicate-detection walk with collectAndValidate (via
// validateEntries) but, unlike that fail-fast `catalog add` path, it reports
// every file so a CI run surfaces every problem in one pass.
func (r *runner) catalogValidate(dir string) error {
	entries, err := validateEntries(dir)
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	if len(entries) == 0 {
		return agentsafety.NewUsageError(fmt.Sprintf("no manifests (*.yaml/*.yml) found in %s", dir))
	}

	results := make([]catalogValidateResult, 0, len(entries))
	for _, e := range entries {
		res := catalogValidateResult{file: e.file, name: e.name}
		switch {
		case e.readErr != nil:
			res.err = e.readErr
		case e.valErr != nil:
			res.err = e.valErr
		case e.dupOf != "":
			res.err = fmt.Errorf("duplicate service name %q (already defined by %s)", e.name, e.dupOf)
		}
		results = append(results, res)
	}

	var failed int
	for _, res := range results {
		if res.err != nil {
			failed++
			_, _ = fmt.Fprintf(r.stdout, "FAIL %s: %v\n", res.file, res.err)
			continue
		}
		_, _ = fmt.Fprintf(r.stdout, "ok   %s (%s)\n", res.file, res.name)
	}
	if failed > 0 {
		return agentsafety.NewUsageError(fmt.Sprintf("%d of %d manifest(s) failed validation in %s", failed, len(results), dir))
	}
	return nil
}

// manifestEntry is one manifest file's outcome from validateEntries: the shared
// read → ValidatePortableManifest → stem-fallback → duplicate-detection walk,
// with the caller-specific error formatting/typing left to the reducer. Exactly
// one of readErr/valErr is set when the file failed; dupOf names an earlier file
// in the same dir that already defined `name`; otherwise `data` holds the bytes.
type manifestEntry struct {
	file    string // base filename
	name    string // resolved service name (filename stem when the manifest is unnamed)
	data    []byte // file bytes (only meaningful for a valid, non-duplicate entry)
	readErr error  // file read failure, pre-formatted as "read <path>: ..."
	valErr  error  // ValidatePortableManifest failure (raw, unwrapped)
	dupOf   string // earlier file that defined `name`, when this entry duplicates it
}

// validateEntries walks the top-level *.yaml/*.yml files in dir (sorted) and
// validates each as a portable manifest, returning one manifestEntry per file.
// It never fails fast and never wraps errors into a caller-specific type, so
// both the fail-fast `catalog add` path (collectAndValidate) and the
// accumulate-all `catalog validate` path reduce the identical walk differently.
// Only the dir-read error is returned directly (the callers wrap it with their
// own source/dir label).
func validateEntries(dir string) ([]manifestEntry, error) {
	entries, err := yamlEntriesIn(dir)
	if err != nil {
		return nil, err
	}
	out := make([]manifestEntry, 0, len(entries))
	svcToFile := map[string]string{} // service name → first file that defined it
	for _, e := range entries {
		me := manifestEntry{file: e.Name()}
		path := filepath.Join(dir, e.Name())
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			me.readErr = fmt.Errorf("read %s: %w", path, readErr)
			out = append(out, me)
			continue
		}
		svcName, valErr := manifest.ValidatePortableManifest(b)
		if valErr != nil {
			me.valErr = valErr
			out = append(out, me)
			continue
		}
		if svcName == "" {
			svcName = strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".yaml"), ".yml")
		}
		me.name = svcName
		if prev, dup := svcToFile[svcName]; dup {
			me.dupOf = prev
			out = append(out, me)
			continue
		}
		svcToFile[svcName] = e.Name()
		me.data = b
		out = append(out, me)
	}
	return out, nil
}

// yamlEntriesIn returns the top-level *.yaml/*.yml directory entries directly
// under dir, sorted by name (os.ReadDir already returns entries sorted by
// filename, so this is just the shared filter both collectAndValidate and
// catalogValidate need).
func yamlEntriesIn(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	yamlEntries := make([]os.DirEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !isYAMLFile(e.Name()) {
			continue
		}
		yamlEntries = append(yamlEntries, e)
	}
	sort.Slice(yamlEntries, func(i, j int) bool { return yamlEntries[i].Name() < yamlEntries[j].Name() })
	return yamlEntries, nil
}
