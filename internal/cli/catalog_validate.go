package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
// per-file result to stdout. Unlike collectAndValidate (which fails closed on
// the FIRST bad manifest, the right behavior for `catalog add`), this walks
// every file so a CI run reports every problem in one pass rather than one
// file at a time.
func (r *runner) catalogValidate(dir string) error {
	entries, err := yamlEntriesIn(dir)
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	if len(entries) == 0 {
		return &usageError{fmt.Sprintf("no manifests (*.yaml/*.yml) found in %s", dir)}
	}

	results := make([]catalogValidateResult, 0, len(entries))
	svcToFile := map[string]string{} // service name → first file that defined it
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			results = append(results, catalogValidateResult{file: e.Name(), err: fmt.Errorf("read %s: %w", path, readErr)})
			continue
		}
		svcName, valErr := manifest.ValidatePortableManifest(b)
		if valErr != nil {
			results = append(results, catalogValidateResult{file: e.Name(), err: valErr})
			continue
		}
		if svcName == "" {
			svcName = strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".yaml"), ".yml")
		}
		if prev, dup := svcToFile[svcName]; dup {
			results = append(results, catalogValidateResult{
				file: e.Name(),
				name: svcName,
				err:  fmt.Errorf("duplicate service name %q (already defined by %s)", svcName, prev),
			})
			continue
		}
		svcToFile[svcName] = e.Name()
		results = append(results, catalogValidateResult{file: e.Name(), name: svcName})
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
		return &usageError{fmt.Sprintf("%d of %d manifest(s) failed validation in %s", failed, len(results), dir)}
	}
	return nil
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
