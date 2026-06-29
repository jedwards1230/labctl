package cli

import (
	"fmt"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/spf13/cobra"
)

// cmdCatalog inspects the built-in (embedded) manifest catalog: the portable
// manifests compiled into the binary. They are the default set of services — a
// local services/<name>.yaml overrides one by name, but absent any local file
// every catalog service is still available, so consumers need not vendor copies.
func (r *runner) cmdCatalog() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "inspect the built-in (embedded) service manifest catalog",
		Long: "Inspect the portable manifests compiled into the binary.\n\n" +
			"These are the default catalog: a local services/<name>.yaml overrides the\n" +
			"embedded manifest of the same name, but absent any local file every catalog\n" +
			"service is still available. `catalog show <name>` dumps one to stdout so you\n" +
			"can fork it into a local override.",
	}
	cmd.AddCommand(r.cmdCatalogList())
	cmd.AddCommand(r.cmdCatalogShow())
	return cmd
}

func (r *runner) cmdCatalogList() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list the embedded catalog services",
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
		Short: "print an embedded manifest's YAML (fork it into a local override)",
		Args:  cobra.ExactArgs(1),
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
