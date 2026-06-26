package cli

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jedwards1230/labctl/internal/command"
	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/mcpserver"
	"github.com/spf13/cobra"
)

func (r *runner) addBuiltins(root *cobra.Command, loaded *manifest.Loaded, loadErr error) {
	root.AddCommand(r.cmdInit())
	root.AddCommand(r.cmdList(loaded, loadErr))
	root.AddCommand(r.cmdLint(loaded, loadErr))
	root.AddCommand(r.cmdDoctor(loaded))
	root.AddCommand(r.cmdMCP())
	root.AddCommand(r.cmdVersion())
	root.AddCommand(r.cmdSelfUpdate())
}

func (r *runner) cmdList(loaded *manifest.Loaded, loadErr error) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list configured services",
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.listServices(loaded, loadErr)
		},
	}
}

// listServices prints the configured services (name + optional description) to
// stdout. Shared by the `list` builtin and bare `labctl svc`, so both stay in
// lockstep. An empty config dir is not an error.
func (r *runner) listServices(loaded *manifest.Loaded, loadErr error) error {
	if loadErr != nil {
		return loadErr
	}
	if loaded == nil || len(loaded.Services) == 0 {
		_, _ = fmt.Fprintf(r.stdout, "No services configured. Add manifests under %s/services/\n", manifest.ConfigDir())
		return nil
	}
	for _, name := range loaded.SortedServiceNames() {
		svc := loaded.Services[name]
		if svc.Description != "" {
			_, _ = fmt.Fprintf(r.stdout, "%-14s %s\n", name, svc.Description)
		} else {
			_, _ = fmt.Fprintln(r.stdout, name)
		}
	}
	return nil
}

func (r *runner) cmdLint(loaded *manifest.Loaded, loadErr error) *cobra.Command {
	return &cobra.Command{
		Use:   "lint [service|path.yaml]",
		Short: "validate manifest schema",
		RunE: func(cmd *cobra.Command, args []string) error {
			r.curCommand = "lint"
			// A failed load (e.g. invalid config.yaml) surfaces its real
			// diagnostic and exit code, not a misleading "no manifests loaded".
			if loadErr != nil {
				return loadErr
			}
			// A file path argument: validate that file directly.
			if len(args) == 1 && (strings.HasSuffix(args[0], ".yaml") || strings.HasSuffix(args[0], ".yml")) {
				cfg := manifest.Config{}
				if loaded != nil {
					cfg = loaded.Config
				}
				if _, err := manifest.LoadService(args[0], cfg); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(r.stdout, "ok %s\n", args[0])
				return nil
			}
			if loaded == nil {
				return fmt.Errorf("no manifests loaded")
			}
			names := loaded.SortedServiceNames()
			if len(args) == 1 {
				if _, ok := loaded.Services[args[0]]; !ok {
					return &usageError{fmt.Sprintf("unknown service %q", args[0])}
				}
				names = []string{args[0]}
			}
			for _, name := range names {
				if err := manifest.Validate(loaded.Services[name]); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				_, _ = fmt.Fprintf(r.stdout, "ok %s\n", name)
			}
			return nil
		},
	}
}

func (r *runner) cmdDoctor(loaded *manifest.Loaded) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor [service]",
		Short: "probe service reachability (drift check)",
		RunE: func(cmd *cobra.Command, args []string) error {
			r.curCommand = "doctor"
			if loaded == nil || len(loaded.Services) == 0 {
				return fmt.Errorf("no services configured")
			}
			names := loaded.SortedServiceNames()
			if len(args) == 1 {
				if _, ok := loaded.Services[args[0]]; !ok {
					return &usageError{fmt.Sprintf("unknown service %q", args[0])}
				}
				names = []string{args[0]}
			}
			for _, name := range names {
				svc := loaded.Services[name]
				_, _ = fmt.Fprintf(r.stdout, "%-14s %s\n", name, probe(svc))
			}
			return nil
		},
	}
}

// probe does a cheap, unauthenticated reachability check of base_url. It reports
// reachability only — auth is not exercised (that needs a real command). It
// builds a per-service client honoring tls_insecure so a self-signed service
// does not always report "unreachable: x509".
func probe(svc *manifest.Service) string {
	if reason, skip := probeSkip(svc); skip {
		return reason
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if svc.TLSInsecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // opt-in per manifest tls_insecure
	}
	resp, err := client.Get(svc.BaseURL)
	if err != nil {
		return "unreachable: " + err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	return fmt.Sprintf("reachable (HTTP %d)", resp.StatusCode)
}

// probeSkip classifies whether a service can be reachability-probed over plain
// HTTP, returning a reason and skip=true when not (empty/templated base, a
// websocket/jsonrpc-ws endpoint). Pure and unit-testable — no network.
func probeSkip(svc *manifest.Service) (string, bool) {
	base := svc.BaseURL
	if base == "" || strings.Contains(base, "{") || strings.HasPrefix(base, "wss") || svc.Transport == "jsonrpc-ws" {
		return "skipped (no probeable http base_url)", true
	}
	return "", false
}

func (r *runner) cmdMCP() *cobra.Command {
	var readOnly bool
	var services []string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "serve manifests as MCP tools over stdio",
		Long: "Serve every non-ignored command as an MCP tool over stdio.\n\n" +
			"--read-only omits write tools entirely; --service restricts the tool set\n" +
			"to the named service(s). Both filters compose.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if r.loaded == nil || len(r.loaded.Services) == 0 {
				return fmt.Errorf("no services configured; add manifests under %s/services/", manifest.ConfigDir())
			}
			if err := mcpserver.ValidateServices(r.loaded, services); err != nil {
				return &usageError{err.Error()}
			}
			opts := mcpserver.Options{ReadOnly: readOnly, Services: services}
			return mcpserver.Serve(cmd.Context(), r.loaded, r.config, Version, r.tracer, r.stderr, opts)
		},
	}
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "expose only read tools; skip every write command")
	cmd.Flags().StringSliceVar(&services, "service", nil, "restrict tools to these service(s); repeatable or comma-separated (default: all)")
	return cmd
}

func (r *runner) cmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print the labctl version",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _ = fmt.Fprintln(r.stdout, "labctl", Version)
			return nil
		},
	}
}

// serviceHelp renders the Long help for a service: description + its commands.
func serviceHelp(svc *manifest.Service, cmds map[string]*command.Command) string {
	var b strings.Builder
	if svc.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", svc.Description)
	}
	if len(cmds) > 0 {
		b.WriteString("Commands:\n")
		for _, id := range command.SortedIDs(cmds) {
			c := cmds[id]
			mark := ""
			if c.Write {
				mark = " (write)"
			}
			fmt.Fprintf(&b, "  %-16s %s%s\n", id, c.Help, mark)
		}
		b.WriteString("\n")
	}
	verbs := make([]string, 0, len(command.HTTPVerbs))
	for v := range command.HTTPVerbs {
		verbs = append(verbs, v)
	}
	sort.Strings(verbs)
	fmt.Fprintf(&b, "Generic verbs: %s", strings.Join(verbs, " "))
	if svc.Transport == "jsonrpc-ws" {
		b.WriteString(" call")
	}
	b.WriteString("\n")
	if svc.EnvPrefix != "" {
		fmt.Fprintf(&b, "\nEnv overrides: %s_URL, %s_<SECRET>\n", svc.EnvPrefix, svc.EnvPrefix)
	}
	return b.String()
}
