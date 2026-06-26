package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/spf13/cobra"
)

// cmdInit scaffolds a commented starter manifest for a new service. It prints to
// stdout by default, or writes to --output (refusing to clobber unless --force).
func (r *runner) cmdInit() *cobra.Command {
	var auth string
	var outPath string
	var force bool
	cmd := &cobra.Command{
		Use:   "init <service>",
		Short: "scaffold a commented starter manifest for a new service",
		Long: "Emit a commented starter manifest that teaches the schema.\n\n" +
			"By default the template prints to stdout; use -o to write it to a file.\n" +
			"The output validates cleanly (`labctl lint <file>`).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r.curCommand = "init"
			name := args[0]
			tmpl, err := manifest.Scaffold(name, auth)
			if err != nil {
				return &usageError{err.Error()}
			}
			if outPath == "" {
				_, _ = fmt.Fprint(r.stdout, tmpl)
				return nil
			}
			if !force {
				if _, statErr := os.Stat(outPath); statErr == nil {
					return &usageError{fmt.Sprintf("%s already exists; pass --force to overwrite", outPath)}
				}
			}
			if err := os.WriteFile(outPath, []byte(tmpl), 0o600); err != nil {
				return fmt.Errorf("writing %s: %w", outPath, err)
			}
			_, _ = fmt.Fprintf(r.stderr, "wrote %s\n", outPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&auth, "auth", manifest.DefaultScaffoldAuth,
		"auth scheme for the stanza: "+strings.Join(manifest.ScaffoldAuthSchemes, "|"))
	cmd.Flags().StringVarP(&outPath, "output", "o", "", "write the template to a file instead of stdout")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite the output file if it already exists")
	return cmd
}
