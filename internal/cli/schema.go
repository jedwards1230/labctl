package cli

import (
	"github.com/jedwards1230/labctl/schema"
	"github.com/spf13/cobra"
)

// cmdSchema prints the embedded JSON Schema (draft-07) for a portable service
// manifest to stdout. Pipe it to a file and point an editor's yaml-language-server
// at it for completion + validation while authoring services/<name>.yaml.
func (r *runner) cmdSchema() *cobra.Command {
	return &cobra.Command{
		Use:   "schema",
		Short: "print the manifest JSON Schema (draft-07)",
		Long: "Print the JSON Schema (draft-07) describing a portable service manifest.\n\n" +
			"Pipe it to a file and wire it into your editor via a yaml-language-server\n" +
			"modeline at the top of a manifest:\n\n" +
			"  labctl schema > manifest.schema.json\n" +
			"  # then add this as the first line of services/<name>.yaml:\n" +
			"  # yaml-language-server: $schema=./manifest.schema.json\n\n" +
			"It describes the PORTABLE manifest shape only — base_url and secret refs\n" +
			"bind in profile.yaml, not in a manifest.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r.curCommand = "schema"
			_, _ = r.stdout.Write(schema.Manifest)
			return nil
		},
	}
}
