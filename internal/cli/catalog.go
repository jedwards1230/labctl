package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/spf13/cobra"
)

// serviceNamePattern restricts a service name to a single, safe path segment: it
// must start with an alphanumeric and contain only lowercase letters, digits, '-'
// and '_'. Every real catalog stem matches (authentik, n8n, ts, radarr, …). It
// rejects "", ".", "..", "a/b", and "../../etc/passwd" outright.
var serviceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// validateServiceName guards the catalog edit/vendor path construction against
// traversal: name is unchecked CLI input used to build <config-dir>/services/
// <name>.yaml and catalog/<name>.yaml, so it must be a single path segment that
// cannot contain a separator or climb out of those directories. Called FIRST in
// both commands, before any path is built. Returns a usageError (exit 2).
func validateServiceName(name string) error {
	if !serviceNamePattern.MatchString(name) {
		return &usageError{fmt.Sprintf("invalid service name %q: must be a single path segment (^[a-z0-9][a-z0-9_-]*$)", name)}
	}
	return nil
}

// cmdCatalog inspects, edits, and vendors the built-in (embedded) manifest
// catalog: the portable manifests compiled into the binary. They are the default
// set of services — a local services/<name>.yaml overrides one by name, but
// absent any local file every catalog service is still available, so consumers
// need not vendor copies.
//
// The authoring loop lives here: `catalog edit <name>` seeds an embedded manifest
// into the local override dir so you can iterate live (the override shadows the
// embedded one at the next load — no rebuild), and `catalog vendor <name>`
// promotes that edited override back into the repo's catalog/ source tree to ship
// it embedded in the next release.
func (r *runner) cmdCatalog() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "inspect, edit, and vendor the built-in service manifest catalog",
		Long: "Inspect, edit, and vendor the portable manifests compiled into the binary.\n\n" +
			"These are the default catalog: a local services/<name>.yaml overrides the\n" +
			"embedded manifest of the same name, but absent any local file every catalog\n" +
			"service is still available.\n\n" +
			"Authoring loop (rebuild-free): `catalog edit <name>` copies an embedded\n" +
			"manifest into <config-dir>/services/<name>.yaml, where it shadows the embedded\n" +
			"one at the next load — edit it and re-run with no recompile. When it's right,\n" +
			"`catalog vendor <name>` promotes the override back into the repo's catalog/\n" +
			"source tree to commit and ship it embedded.",
	}
	cmd.AddCommand(r.cmdCatalogList())
	cmd.AddCommand(r.cmdCatalogShow())
	cmd.AddCommand(r.cmdCatalogEdit())
	cmd.AddCommand(r.cmdCatalogVendor())
	cmd.AddCommand(r.cmdCatalogAdd())
	cmd.AddCommand(r.cmdCatalogUpdate())
	cmd.AddCommand(r.cmdCatalogRemove())
	cmd.AddCommand(r.cmdCatalogInstalled())
	cmd.AddCommand(r.cmdCatalogValidate())
	return cmd
}

func (r *runner) cmdCatalogList() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list the embedded catalog services (excludes local-only and override markers)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r.curCommand = "catalog"
			svcs, err := manifest.CatalogServices()
			if err != nil {
				return err
			}
			for _, svc := range svcs {
				if svc.Description != "" {
					_, _ = fmt.Fprintf(r.stdout, "%-14s %s\n", svc.Name, svc.Description)
				} else {
					_, _ = fmt.Fprintln(r.stdout, svc.Name)
				}
			}
			return nil
		},
	}
}

func (r *runner) cmdCatalogShow() *cobra.Command {
	return &cobra.Command{
		Use:   "show <service>",
		Short: "print an embedded manifest's YAML to stdout",
		Long: "Print an embedded manifest's YAML to stdout.\n\n" +
			"To seed a local override for live editing, use `labctl catalog edit <name>`\n" +
			"(it copies the complete manifest into <config-dir>/services/<name>.yaml, where\n" +
			"it shadows the embedded one at the next load — no recompile required).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r.curCommand = "catalog"
			data, ok := manifest.CatalogManifest(args[0])
			if !ok {
				return &usageError{fmt.Sprintf("no embedded service %q (see `labctl catalog list`)", args[0])}
			}
			_, _ = r.stdout.Write(data)
			return nil
		},
	}
}

// cmdCatalogEdit copies an embedded manifest into the local override dir so the
// user can iterate on it live (rebuild-free). The override shadows the embedded
// manifest by name at the next load.
func (r *runner) cmdCatalogEdit() *cobra.Command {
	var force, open bool
	cmd := &cobra.Command{
		Use:   "edit <service>",
		Short: "copy an embedded manifest into the local override dir for live editing",
		Long: "Copy an embedded manifest into <config-dir>/services/<name>.yaml, where it\n" +
			"shadows the embedded one at the next load — edit it and re-run with no\n" +
			"recompile.\n\n" +
			"A FULL copy of the manifest is seeded (not a sparse patch): a local override\n" +
			"WHOLESALE REPLACES the embedded entry — it is validated standalone, with no\n" +
			"field-level merge — so a partial override would drop endpoints or fail\n" +
			"validation. The complete manifest is written so it loads as-is.\n\n" +
			"On success the absolute path written is printed to stdout (compose it, e.g.\n" +
			"`$EDITOR $(labctl catalog edit authentik)`). An existing override is not\n" +
			"clobbered without --force. With --edit, $VISUAL/$EDITOR is opened on the file\n" +
			"after writing.\n\n" +
			"When the manifest is right, `labctl catalog vendor <name>` promotes it back\n" +
			"into the repo's catalog/ source tree to ship embedded.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r.curCommand = "catalog"
			return r.catalogEdit(args[0], force, open)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing override file")
	cmd.Flags().BoolVar(&open, "edit", false, "open $VISUAL/$EDITOR on the file after writing")
	return cmd
}

// catalogEdit seeds the FULL embedded manifest for name into the local override
// dir. A full copy is required because a local override wholesale replaces the
// embedded entry (no field-level merge); see cmdCatalogEdit's Long help.
func (r *runner) catalogEdit(name string, force, open bool) error {
	if err := validateServiceName(name); err != nil {
		return err
	}
	data, ok := manifest.CatalogManifest(name)
	if !ok {
		return &usageError{fmt.Sprintf("no embedded service %q (see `labctl catalog list`)", name)}
	}
	svcDir := filepath.Join(r.configDir(), "services")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", svcDir, err)
	}
	dest := filepath.Join(svcDir, name+".yaml")
	if !force {
		if _, statErr := os.Stat(dest); statErr == nil {
			return &usageError{fmt.Sprintf("%s already exists; pass --force to overwrite", dest)}
		}
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	abs := absOrSelf(dest)
	_, _ = fmt.Fprintln(r.stdout, abs)
	if open {
		if err := launchEditor(abs); err != nil {
			// Opening the editor is best-effort sugar; the file is already
			// written and its path printed, so warn rather than fail.
			_, _ = fmt.Fprintf(r.stderr, "labctl: %v\n", err)
		}
	}
	return nil
}

// cmdCatalogVendor promotes a local override back into the repo's catalog/ source
// tree, validating it first so a broken manifest is never shipped.
func (r *runner) cmdCatalogVendor() *cobra.Command {
	var force bool
	var catalogDir string
	cmd := &cobra.Command{
		Use:   "vendor <service>",
		Short: "promote a local override into the repo catalog/ source tree",
		Long: "Promote a local override (<config-dir>/services/<name>.yaml — typically one\n" +
			"seeded by `labctl catalog edit`) back into the repo's catalog/ source tree at\n" +
			"catalog/<name>.yaml, ready to commit and ship embedded in the next release.\n\n" +
			"vendor is a maintainer command run from a labctl repo checkout. The running\n" +
			"binary can't know the repo path, so the destination is catalog/ relative to\n" +
			"the current directory by default; pass --catalog-dir to point elsewhere.\n\n" +
			"The override is validated before vendoring (it must be a well-formed portable\n" +
			"manifest — no base_url, no secret refs), so a broken manifest is never\n" +
			"promoted. An existing catalog/<name>.yaml is not clobbered without --force.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r.curCommand = "catalog"
			return r.catalogVendor(args[0], catalogDir, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing catalog/<name>.yaml")
	cmd.Flags().StringVar(&catalogDir, "catalog-dir", "catalog", "destination catalog/ dir in a labctl repo checkout (default: catalog/ relative to cwd; run from the repo root)")
	return cmd
}

// catalogVendor copies a validated local override into catalogDir/<name>.yaml.
func (r *runner) catalogVendor(name, catalogDir string, force bool) error {
	if err := validateServiceName(name); err != nil {
		return err
	}
	src := filepath.Join(r.configDir(), "services", name+".yaml")
	if _, statErr := os.Stat(src); statErr != nil {
		if os.IsNotExist(statErr) {
			return &usageError{fmt.Sprintf("no local override %s (run `labctl catalog edit %s` first)", src, name)}
		}
		return fmt.Errorf("stat %s: %w", src, statErr)
	}
	// Validate before vendoring — never promote a broken manifest. LoadService
	// runs structural Validate, which is exactly right for a portable catalog
	// manifest (it rejects an in-manifest base_url or secret ref).
	if _, err := manifest.LoadService(src, r.config); err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", catalogDir, err)
	}
	dest := filepath.Join(catalogDir, name+".yaml")
	if !force {
		if _, statErr := os.Stat(dest); statErr == nil {
			return &usageError{fmt.Sprintf("%s already exists; pass --force to overwrite", dest)}
		}
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	_, _ = fmt.Fprintln(r.stdout, absOrSelf(dest))
	return nil
}

// absOrSelf returns the absolute form of path, falling back to path itself if the
// cwd can't be resolved (so we always print something usable).
func absOrSelf(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// launchEditor opens $VISUAL (then $EDITOR) on path, wired to the real process
// stdio so the interactive editor works. The editor var may carry args (e.g.
// "code --wait"), so it is split on whitespace.
func launchEditor(path string) error {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if strings.TrimSpace(editor) == "" {
		return fmt.Errorf("neither $VISUAL nor $EDITOR is set; the file is at %s", path)
	}
	parts := strings.Fields(editor)
	args := append(parts[1:], path)
	c := exec.Command(parts[0], args...) // #nosec G204 -- editor is the user's own $VISUAL/$EDITOR
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}
