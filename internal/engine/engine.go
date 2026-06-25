// Package engine resolves a Command against a Service into a concrete request,
// dispatches it over the service's transport, and returns the raw body plus the
// resolved output spec for rendering. It owns template expansion, endpoint
// selection, auth wiring, and (Phase 1) none/fixed-query pagination.
package engine

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/jedwards1230/labctl/internal/auth"
	"github.com/jedwards1230/labctl/internal/command"
	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/secret"
	"github.com/jedwards1230/labctl/internal/template"
	"github.com/jedwards1230/labctl/internal/transport"
)

// Flags are the universal CLI flags that influence a request.
type Flags struct {
	Filter   string
	Raw      bool
	Query    string
	Limit    int
	Output   string
	Endpoint string
	DryRun   bool
	Verbose  bool
}

// Request bundles everything needed to execute one command.
type Request struct {
	Config  manifest.Config
	Service *manifest.Service
	Command *command.Command
	Args    []string // positional args after the command selector
	Flags   Flags
	Runner  secret.Runner // nil = real `op`
	Getenv  func(string) string
}

// Result is the outcome of an execution.
type Result struct {
	Body      []byte          // response body (nil on dry-run)
	Output    manifest.Output // resolved filter/mode for rendering
	DryRunMsg string          // populated when Flags.DryRun
}

// Execute runs the command. stderr receives verbose diagnostics.
func Execute(req Request, stderr io.Writer) (*Result, error) {
	getenv := req.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	svc := req.Service
	cmd := req.Command

	transportKind := svc.Transport
	if transportKind == "" {
		transportKind = "http"
	}
	if transportKind != "http" {
		return nil, fmt.Errorf("transport %q is not yet implemented (planned for a later phase)", transportKind)
	}

	// Resolve the target endpoint (default or named).
	ep, err := resolveEndpoint(svc, cmd, req.Flags.Endpoint)
	if err != nil {
		return nil, err
	}

	// Template env: vars (env-overridable) + args + secret resolver.
	vars := envOverrideVars(svc, getenv)
	res := secret.New(req.Config.Secret, svc.Secrets, svc.EnvPrefix, req.Runner)
	tmplEnv := template.Env{Vars: vars, Args: req.Args, Secrets: res, Getenv: getenv}

	base, err := resolveBaseURL(ep.BaseURL, svc, vars, tmplEnv, getenv)
	if err != nil {
		return nil, err
	}

	path, err := tmplEnv.Expand(cmd.Path)
	if err != nil {
		return nil, fmt.Errorf("expand path: %w", err)
	}
	url := joinURL(base, path)

	query, err := buildQuery(cmd, tmplEnv, req.Flags)
	if err != nil {
		return nil, err
	}
	if query != "" {
		url += "?" + query
	}

	headers, err := expandHeaders(cmd.Headers, tmplEnv)
	if err != nil {
		return nil, err
	}

	body, contentType, err := resolveBody(cmd, tmplEnv)
	if err != nil {
		return nil, err
	}

	authSpec := svc.Auth
	if ep.Auth != nil {
		authSpec = *ep.Auth
	}
	applier := auth.New(authSpec, tmplEnv)

	if req.Flags.DryRun {
		preview := mergeAuthPreview(headers, authSpec, cmd.NoAuth)
		return &Result{DryRunMsg: dryRun(cmd.Method, url, preview, body), Output: cmd.Output}, nil
	}

	var verbose io.Writer
	if req.Flags.Verbose {
		verbose = stderr
	}

	respBody, err := transport.DoHTTP(transport.HTTPRequest{
		Method:      cmd.Method,
		URL:         url,
		Headers:     headers,
		Body:        body,
		ContentType: contentType,
		TLSInsecure: ep.TLSInsecure || svc.TLSInsecure,
		Timeout:     svc.TimeoutDuration(),
		Auth:        applier,
		NoAuth:      cmd.NoAuth,
		Verbose:     verbose,
	})
	if err != nil {
		return nil, err
	}
	return &Result{Body: respBody, Output: cmd.Output}, nil
}

// resolvedEndpoint is the flattened connection target for a command.
type resolvedEndpoint struct {
	BaseURL     string
	Auth        *manifest.Auth
	TLSInsecure bool
}

func resolveEndpoint(svc *manifest.Service, cmd *command.Command, flagEndpoint string) (resolvedEndpoint, error) {
	name := flagEndpoint
	if name == "" {
		name = cmd.Endpoint
	}
	if name == "" {
		return resolvedEndpoint{BaseURL: svc.BaseURL}, nil
	}
	ep, ok := svc.Endpoints[name]
	if !ok {
		return resolvedEndpoint{}, fmt.Errorf("unknown endpoint %q", name)
	}
	return resolvedEndpoint{BaseURL: ep.BaseURL, Auth: ep.Auth, TLSInsecure: ep.TLSInsecure}, nil
}

// envOverrideVars copies svc.Vars, letting <PREFIX>_<VAR> env override each.
func envOverrideVars(svc *manifest.Service, getenv func(string) string) map[string]string {
	vars := make(map[string]string, len(svc.Vars))
	for k, v := range svc.Vars {
		vars[k] = v
		if svc.EnvPrefix != "" {
			if ov := getenv(strings.ToUpper(svc.EnvPrefix + "_" + k)); ov != "" {
				vars[k] = ov
			}
		}
	}
	return vars
}

// resolveBaseURL honors a <PREFIX>_URL whole-base override, else expands the
// templated base_url.
func resolveBaseURL(epBase string, svc *manifest.Service, vars map[string]string, env template.Env, getenv func(string) string) (string, error) {
	// A named endpoint's base_url is not overridden by <PREFIX>_URL (that targets
	// the default base).
	if epBase == svc.BaseURL && svc.EnvPrefix != "" {
		if ov := getenv(strings.ToUpper(svc.EnvPrefix) + "_URL"); ov != "" {
			return env.Expand(ov)
		}
	}
	return env.Expand(epBase)
}

func buildQuery(cmd *command.Command, env template.Env, flags Flags) (string, error) {
	var parts []string
	if cmd.Query != "" {
		q, err := env.Expand(cmd.Query)
		if err != nil {
			return "", fmt.Errorf("expand query: %w", err)
		}
		parts = append(parts, strings.TrimPrefix(q, "?"))
	}
	// Pagination: Phase 1 supports fixed-query (append) and none.
	switch cmd.Pagination.Style {
	case "fixed-query":
		if cmd.Pagination.Query != "" {
			parts = append(parts, strings.TrimPrefix(cmd.Pagination.Query, "?"))
		}
	}
	if flags.Query != "" {
		parts = append(parts, strings.TrimPrefix(flags.Query, "?"))
	}
	if flags.Limit > 0 {
		parts = append(parts, "limit="+strconv.Itoa(flags.Limit))
	}
	return strings.Join(filterEmpty(parts), "&"), nil
}

func expandHeaders(in map[string]string, env template.Env) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		ev, err := env.Expand(v)
		if err != nil {
			return nil, fmt.Errorf("expand header %q: %w", k, err)
		}
		out[k] = ev
	}
	return out, nil
}

func resolveBody(cmd *command.Command, env template.Env) ([]byte, string, error) {
	if cmd.Body == "" {
		return nil, "", nil
	}
	if strings.HasPrefix(cmd.Body, "@") {
		path := cmd.Body[1:]
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("read body file %q: %w", path, err)
		}
		return b, contentTypeFor(cmd.Codec), nil
	}
	expanded, err := env.Expand(cmd.Body)
	if err != nil {
		return nil, "", fmt.Errorf("expand body: %w", err)
	}
	return []byte(expanded), contentTypeFor(cmd.Codec), nil
}

func contentTypeFor(c manifest.Codec) string {
	switch c.Request {
	case "form":
		return "application/x-www-form-urlencoded"
	default:
		return "application/json"
	}
}

func joinURL(base, path string) string {
	if path == "" {
		return base
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

// mergeAuthPreview adds a redacted line for the credential the auth strategy
// would set, so --dry-run shows it WITHOUT resolving the secret (no op call).
func mergeAuthPreview(headers map[string]string, a manifest.Auth, noAuth bool) map[string]string {
	out := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		out[k] = v
	}
	if noAuth {
		return out
	}
	switch a.Strategy {
	case "header-key":
		out[a.Header] = "<redacted>"
	case "bearer":
		scheme := a.Scheme
		if scheme == "" {
			scheme = "Bearer"
		}
		out["Authorization"] = scheme + " <redacted>"
	case "basic":
		out["Authorization"] = "Basic <redacted>"
	}
	return out
}

func dryRun(method, url string, headers map[string]string, body []byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", strings.ToUpper(method), url)
	for k, v := range headers {
		fmt.Fprintf(&b, "%s: %s\n", k, transport.RedactHeader(k, v))
	}
	if len(body) > 0 {
		fmt.Fprintf(&b, "\n%s\n", string(body))
	}
	return b.String()
}

func filterEmpty(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
