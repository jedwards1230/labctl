// Package cli wires the cobra command tree from the loaded manifests. Each
// service becomes a subcommand; each named command and generic verb becomes a
// leaf. The CLI re-reads manifests just-in-time per invocation (no daemon).
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jedwards1230/labctl/internal/command"
	"github.com/jedwards1230/labctl/internal/engine"
	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/output"
	"github.com/jedwards1230/labctl/internal/telemetry"
	"github.com/jedwards1230/labctl/internal/transport"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Version is set at build time via -ldflags.
var Version = "dev"

type globalFlags struct {
	configDir  string
	filter     string
	raw        bool
	query      string
	limit      int
	output     string
	endpoint   string
	dryRun     bool
	verbose    bool
	jsonErrors bool
	yes        bool
}

type runner struct {
	flags  globalFlags
	stdout io.Writer
	stderr io.Writer
	config manifest.Config
	loaded *manifest.Loaded
	tracer trace.Tracer

	curService string
	curCommand string
	runner     interface{} // reserved for test secret-runner injection
}

// Run builds the command tree and executes it, returning a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	// Optional OpenTelemetry — no-op unless OTEL_* env configures an endpoint.
	tracer, shutdown := telemetry.Start(context.Background(), Version)
	defer shutdown()

	r := &runner{stdout: stdout, stderr: stderr, tracer: tracer}
	root := r.newRoot()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		return reportError(stderr, err, r.flags.jsonErrors, r.curService, r.curCommand)
	}
	return exitOK
}

func (r *runner) newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "labctl",
		Short:         "Manifest-driven CLI for homelab service APIs",
		Long:          "labctl executes one HTTP/RPC call against a service described by a YAML manifest.\nAdding a service is a manifest edit, never a recompile.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       Version,
	}
	pf := root.PersistentFlags()
	pf.StringVar(&r.flags.configDir, "config-dir", "", "config dir (default: $XDG_CONFIG_HOME/labctl or ~/.config/labctl)")
	pf.StringVar(&r.flags.filter, "filter", "", "jq filter over the response (overrides the command default)")
	pf.BoolVar(&r.flags.raw, "raw", false, "print the raw response, no jq filtering")
	pf.StringVar(&r.flags.query, "query", "", "extra query string appended to the request")
	pf.IntVar(&r.flags.limit, "limit", 0, "bound the item count (adds ?limit=N)")
	pf.StringVarP(&r.flags.output, "output", "o", "", "output mode: json|raw|scalar")
	pf.StringVar(&r.flags.endpoint, "endpoint", "", "target a named endpoint")
	pf.BoolVar(&r.flags.dryRun, "dry-run", false, "resolve and print the request, send nothing")
	pf.BoolVarP(&r.flags.verbose, "verbose", "v", false, "request/response diagnostics to stderr (secrets redacted)")
	pf.BoolVar(&r.flags.jsonErrors, "json-errors", false, "emit errors as a JSON envelope")
	pf.BoolVarP(&r.flags.yes, "yes", "y", false, "skip write confirmation (reserved; the binary gates nothing)")

	// Load manifests for dynamic registration. A load error still lets builtins
	// like `lint` run, so report it lazily rather than aborting here.
	loaded, loadErr := manifest.Load(configDirFromArgs(r.flags.configDir, root))
	if loaded != nil {
		r.config = loaded.Config
		r.loaded = loaded
	}

	r.addBuiltins(root, loaded, loadErr)
	if loaded != nil {
		for _, name := range loaded.SortedServiceNames() {
			root.AddCommand(r.newServiceCmd(loaded.Services[name]))
		}
	}
	return root
}

func (r *runner) newServiceCmd(svc *manifest.Service) *cobra.Command {
	cmds := command.FromManifest(svc)
	sc := &cobra.Command{
		Use:   svc.Name,
		Short: svc.Description,
		Long:  serviceHelp(svc, cmds),
	}

	// Named commands.
	for _, id := range command.SortedIDs(cmds) {
		c := cmds[id]
		sc.AddCommand(&cobra.Command{
			Use:                id,
			Short:              c.Help,
			DisableFlagParsing: false,
			RunE: func(cmd *cobra.Command, args []string) error {
				return r.execNamed(svc, c, args)
			},
		})
	}

	// Generic verbs (skip any that a named command already defines).
	for verb := range command.HTTPVerbs {
		if _, taken := cmds[verb]; taken {
			continue
		}
		v := verb
		sc.AddCommand(&cobra.Command{
			Use:   v + " <path> [body|query]",
			Short: "generic " + v + " passthrough",
			Args:  cobra.ArbitraryArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return r.execVerb(svc, v, args)
			},
		})
	}
	if svc.Transport == "jsonrpc-ws" {
		sc.AddCommand(&cobra.Command{
			Use:   "call <method> [json-params]",
			Short: "generic jsonrpc passthrough",
			Args:  cobra.ArbitraryArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return r.execVerb(svc, "call", args)
			},
		})
	}
	return sc
}

func (r *runner) execNamed(svc *manifest.Service, c *command.Command, args []string) error {
	r.curService, r.curCommand = svc.Name, c.ID
	return r.dispatch(svc, c, args)
}

func (r *runner) execVerb(svc *manifest.Service, verb string, args []string) error {
	r.curService, r.curCommand = svc.Name, verb
	c, err := command.Verb(svc.Transport, verb, args)
	if err != nil {
		return &usageError{err.Error()}
	}
	// For verbs, positional args beyond the path are consumed by the synthesizer;
	// pass none as templating args.
	return r.dispatch(svc, c, nil)
}

func (r *runner) dispatch(svc *manifest.Service, c *command.Command, args []string) error {
	ctx, span := r.startSpan(svc, c)
	defer span.End()

	res, err := engine.Execute(ctx, engine.Request{
		Config:  r.config,
		Service: svc,
		Command: c,
		Args:    args,
		Flags: engine.Flags{
			Filter:   r.flags.filter,
			Raw:      r.flags.raw,
			Query:    r.flags.query,
			Limit:    r.flags.limit,
			Output:   r.flags.output,
			Endpoint: r.flags.endpoint,
			DryRun:   r.flags.dryRun,
			Verbose:  r.flags.verbose,
			Yes:      r.flags.yes,
		},
		Runner: r.secretRunner(),
	}, r.stderr)
	if err != nil {
		recordSpanError(span, err)
		return err
	}
	if res.DryRunMsg != "" {
		_, _ = fmt.Fprint(r.stdout, res.DryRunMsg)
		span.SetStatus(codes.Ok, "")
		return nil
	}
	if err := output.Render(res.Body, res.Output, output.Options{
		Filter:        r.flags.filter,
		Raw:           r.flags.raw,
		Mode:          r.flags.output,
		ResponseCodec: res.ResponseCodec,
	}, r.stdout); err != nil {
		recordSpanError(span, err)
		return &decodeError{err}
	}
	span.SetStatus(codes.Ok, "")
	return nil
}

// startSpan opens a span for one command execution. With tracing disabled the
// tracer is a no-op, so this is free.
func (r *runner) startSpan(svc *manifest.Service, c *command.Command) (context.Context, trace.Span) {
	ctx, span := r.tracer.Start(context.Background(), svc.Name+" "+c.ID)
	attrs := []attribute.KeyValue{
		attribute.String("labctl.service", svc.Name),
		attribute.String("labctl.command", c.ID),
		attribute.Bool("labctl.write", c.Write),
	}
	if svc.Transport == "jsonrpc-ws" {
		attrs = append(attrs, attribute.String("rpc.method", c.Method))
	} else if c.Method != "" {
		attrs = append(attrs, attribute.String("http.request.method", c.Method))
	}
	span.SetAttributes(attrs...)
	return ctx, span
}

// recordSpanError marks the span failed and attaches an HTTP/RPC status when known.
func recordSpanError(span trace.Span, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	var he *transport.HTTPError
	if errors.As(err, &he) {
		span.SetAttributes(attribute.Int("http.response.status_code", he.Status))
	}
	var re *transport.RPCError
	if errors.As(err, &re) {
		span.SetAttributes(attribute.Int("rpc.grpc_status_code", re.Code))
	}
}

// secretRunner returns nil (real op) unless a test injected one.
func (r *runner) secretRunner() func(argv []string) (string, error) {
	if r.runner == nil {
		return nil
	}
	return r.runner.(func(argv []string) (string, error))
}

// configDirFromArgs peeks at --config-dir before full parse so dynamic
// registration uses the right dir. Falls back to the resolved default.
func configDirFromArgs(flagVal string, root *cobra.Command) string {
	// Pre-parse persistent flags to honor --config-dir during registration.
	if flagVal != "" {
		return flagVal
	}
	for i, a := range os.Args {
		if a == "--config-dir" && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
		if len(a) > len("--config-dir=") && a[:len("--config-dir=")] == "--config-dir=" {
			return a[len("--config-dir="):]
		}
	}
	return manifest.ConfigDir()
}
