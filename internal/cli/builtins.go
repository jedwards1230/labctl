package cli

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jedwards1230/labctl/internal/command"
	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/spf13/cobra"
)

func (r *runner) addBuiltins(root *cobra.Command, loaded *manifest.Loaded, loadErr error) {
	root.AddCommand(r.cmdList(loaded, loadErr))
	root.AddCommand(r.cmdLint(loaded))
	root.AddCommand(r.cmdDoctor(loaded))
	root.AddCommand(r.cmdMCP())
	root.AddCommand(r.cmdVersion())
}

func (r *runner) cmdList(loaded *manifest.Loaded, loadErr error) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list configured services",
		RunE: func(cmd *cobra.Command, args []string) error {
			if loadErr != nil {
				return loadErr
			}
			if loaded == nil || len(loaded.Services) == 0 {
				fmt.Fprintf(r.stdout, "No services configured. Add manifests under %s/services/\n", manifest.ConfigDir())
				return nil
			}
			for _, name := range loaded.SortedServiceNames() {
				svc := loaded.Services[name]
				if svc.Description != "" {
					fmt.Fprintf(r.stdout, "%-14s %s\n", name, svc.Description)
				} else {
					fmt.Fprintln(r.stdout, name)
				}
			}
			return nil
		},
	}
}

func (r *runner) cmdLint(loaded *manifest.Loaded) *cobra.Command {
	return &cobra.Command{
		Use:   "lint [service|path.yaml]",
		Short: "validate manifest schema",
		RunE: func(cmd *cobra.Command, args []string) error {
			r.curCommand = "lint"
			// A file path argument: validate that file directly.
			if len(args) == 1 && (strings.HasSuffix(args[0], ".yaml") || strings.HasSuffix(args[0], ".yml")) {
				cfg := manifest.Config{}
				if loaded != nil {
					cfg = loaded.Config
				}
				if _, err := manifest.LoadService(args[0], cfg); err != nil {
					return err
				}
				fmt.Fprintf(r.stdout, "ok %s\n", args[0])
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
				fmt.Fprintf(r.stdout, "ok %s\n", name)
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
			client := &http.Client{Timeout: 5 * time.Second}
			for _, name := range names {
				svc := loaded.Services[name]
				fmt.Fprintf(r.stdout, "%-14s %s\n", name, probe(client, svc))
			}
			return nil
		},
	}
}

// probe does a cheap, unauthenticated reachability check of base_url. It reports
// reachability only — auth is not exercised (that needs a real command).
func probe(client *http.Client, svc *manifest.Service) string {
	base := svc.BaseURL
	if base == "" || strings.Contains(base, "{") || strings.HasPrefix(base, "wss") || svc.Transport == "jsonrpc-ws" {
		return "skipped (no probeable http base_url)"
	}
	resp, err := client.Get(base)
	if err != nil {
		return "unreachable: " + err.Error()
	}
	defer resp.Body.Close()
	return fmt.Sprintf("reachable (HTTP %d)", resp.StatusCode)
}

func (r *runner) cmdMCP() *cobra.Command {
	return &cobra.Command{
		Use:    "mcp",
		Short:  "serve manifests as MCP tools over stdio",
		Hidden: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("the MCP server is planned for a later phase")
		},
	}
}

func (r *runner) cmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print the labctl version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(r.stdout, "labctl", Version)
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
