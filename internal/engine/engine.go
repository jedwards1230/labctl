// Package engine resolves a Command against a Service into a concrete request,
// dispatches it over the service's transport, and returns the raw body plus the
// resolved output spec for rendering. It owns template expansion, endpoint
// selection, auth wiring, and pagination (none/fixed-query/cursor/page-number/
// page-until-short).
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/itchyny/gojq"
	"github.com/jedwards1230/labctl/internal/auth"
	"github.com/jedwards1230/labctl/internal/command"
	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/secret"
	"github.com/jedwards1230/labctl/internal/template"
	"github.com/jedwards1230/labctl/internal/transport"
)

// maxPages guards against infinite loops in cursor/page-number pagination.
const maxPages = 1000

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
	Body          []byte          // response body (nil on dry-run)
	Output        manifest.Output // resolved filter/mode for rendering
	ResponseCodec string          // "xml", "json", or "" (empty = json default)
	DryRunMsg     string          // populated when Flags.DryRun
}

// Execute runs the command. ctx carries cancellation/trace context; stderr
// receives verbose diagnostics.
func Execute(ctx context.Context, req Request, stderr io.Writer) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
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

	// Resolve the target endpoint (default or named).
	ep, err := resolveEndpoint(svc, cmd, req.Flags.Endpoint)
	if err != nil {
		return nil, err
	}

	// Template env: vars (env-overridable) + args + secret resolver.
	vars := envOverrideVars(svc, getenv)
	res := secret.New(req.Config.Secret, svc.Secrets, svc.EnvPrefix, req.Runner)
	tmplEnv := template.Env{Vars: vars, Args: req.Args, Secrets: res, Getenv: getenv}

	// Dispatch based on transport kind.
	switch transportKind {
	case "", "http":
		return executeHTTP(ctx, req, svc, cmd, ep, tmplEnv, stderr)
	case "jsonrpc-ws":
		return executeJSONRPCWS(ctx, req, svc, cmd, ep, tmplEnv, stderr)
	default:
		return nil, fmt.Errorf("transport %q is not yet implemented", transportKind)
	}
}

// executeHTTP handles the http transport path.
func executeHTTP(ctx context.Context, req Request, svc *manifest.Service, cmd *command.Command, ep resolvedEndpoint, tmplEnv template.Env, stderr io.Writer) (*Result, error) {
	base, err := resolveBaseURL(ep.BaseURL, svc, tmplEnv.Vars, tmplEnv, tmplEnv.Getenv)
	if err != nil {
		return nil, err
	}

	path, err := tmplEnv.Expand(cmd.Path)
	if err != nil {
		return nil, fmt.Errorf("expand path: %w", err)
	}

	// Apply trailing-slash rule BEFORE appending query string.
	if svc.PathRules.TrailingSlash == "before-query" {
		if !strings.HasSuffix(path, "/") {
			path += "/"
		}
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

	// Resolve the response codec: command-level overrides endpoint-level.
	responseCodec := resolveResponseCodec(cmd, ep)

	if req.Flags.DryRun {
		preview := mergeAuthPreview(headers, authSpec, cmd.NoAuth)
		return &Result{DryRunMsg: dryRun(cmd.Method, url, preview, body), Output: cmd.Output, ResponseCodec: responseCodec}, nil
	}

	var verbose io.Writer
	if req.Flags.Verbose {
		verbose = stderr
	}

	// Build the per-page HTTP request template (captures all resolved fields).
	httpReq := transport.HTTPRequest{
		Ctx:         ctx,
		Method:      cmd.Method,
		Headers:     headers,
		Body:        body,
		ContentType: contentType,
		TLSInsecure: ep.TLSInsecure || svc.TLSInsecure,
		Timeout:     svc.TimeoutDuration(),
		Auth:        applier,
		NoAuth:      cmd.NoAuth,
		Verbose:     verbose,
	}

	pg := cmd.Pagination
	respBody, err := executePaginated(ctx, httpReq, url, query, pg, req.Flags)
	if err != nil {
		return nil, err
	}

	return &Result{Body: respBody, Output: cmd.Output, ResponseCodec: responseCodec}, nil
}

// executeJSONRPCWS handles the jsonrpc-ws transport path.
func executeJSONRPCWS(ctx context.Context, req Request, svc *manifest.Service, cmd *command.Command, ep resolvedEndpoint, tmplEnv template.Env, stderr io.Writer) (*Result, error) {
	authSpec := svc.Auth
	if ep.Auth != nil {
		authSpec = *ep.Auth
	}

	// Resolve auth params by expanding each template in the auth spec.
	var resolvedAuthParams []string
	if !cmd.NoAuth {
		for _, p := range authSpec.Params {
			val, err := tmplEnv.Expand(p)
			if err != nil {
				return nil, fmt.Errorf("expand auth param: %w", err)
			}
			resolvedAuthParams = append(resolvedAuthParams, val)
		}
	}

	// Resolve command params (must be a valid JSON array or empty → default []).
	var resolvedParams []byte
	if cmd.Params != "" {
		expanded, err := tmplEnv.Expand(cmd.Params)
		if err != nil {
			return nil, fmt.Errorf("expand params: %w", err)
		}
		resolvedParams = []byte(expanded)
	}

	wsURL, err := resolveBaseURL(ep.BaseURL, svc, tmplEnv.Vars, tmplEnv, tmplEnv.Getenv)
	if err != nil {
		return nil, err
	}

	if req.Flags.DryRun {
		params := string(resolvedParams)
		if params == "" {
			params = "[]"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "WS %s\n", wsURL)
		if !cmd.NoAuth {
			fmt.Fprintf(&b, "auth: %s [\"<redacted>\"]\n", authSpec.Method)
		}
		fmt.Fprintf(&b, "call: %s %s\n", cmd.Method, params)
		return &Result{DryRunMsg: b.String(), Output: cmd.Output}, nil
	}

	var verbose io.Writer
	if req.Flags.Verbose {
		verbose = stderr
	}

	wsReq := transport.JSONRPCWSRequest{
		Ctx:         ctx,
		URL:         wsURL,
		TLSInsecure: ep.TLSInsecure || svc.TLSInsecure,
		Timeout:     svc.TimeoutDuration(),
		AuthMethod:  authSpec.Method,
		AuthParams:  resolvedAuthParams,
		NoAuth:      cmd.NoAuth,
		Method:      cmd.Method,
		Params:      resolvedParams,
		Verbose:     verbose,
	}

	result, err := transport.DoJSONRPCWS(wsReq)
	if err != nil {
		return nil, err
	}

	return &Result{Body: result, Output: cmd.Output}, nil
}

// resolveResponseCodec returns the effective response codec: command wins, then
// endpoint, then "" (which callers treat as JSON).
func resolveResponseCodec(cmd *command.Command, ep resolvedEndpoint) string {
	if cmd.Codec.Response != "" {
		return cmd.Codec.Response
	}
	return ep.Codec.Response
}

// executePaginated issues one or more HTTP requests depending on the pagination
// style and returns the accumulated body. For none/fixed-query it is a single
// call; for cursor/page-number/page-until-short it accumulates across pages and
// synthesizes a merged JSON body.
func executePaginated(
	ctx context.Context,
	base transport.HTTPRequest,
	baseURL, baseQuery string,
	pg manifest.Pagination,
	flags Flags,
) ([]byte, error) {
	_ = ctx // ctx is already embedded in base.Ctx

	switch pg.Style {
	case "", "none", "fixed-query":
		// Single call — URL already built with any fixed-query appended.
		base.URL = baseURL
		return transport.DoHTTP(base)

	case "cursor":
		return fetchCursor(base, baseURL, baseQuery, pg, flags)

	case "page-number":
		return fetchPageNumber(base, baseURL, baseQuery, pg, flags)

	case "page-until-short":
		return fetchPageUntilShort(base, baseURL, baseQuery, pg, flags)

	default:
		// Should not reach here after validation, but be safe.
		base.URL = baseURL
		return transport.DoHTTP(base)
	}
}

// fetchCursor implements cursor-based pagination.
// First request uses no cursor param; subsequent requests set Param=<cursor value>.
// Stops when the Next jq path returns null/empty/absent.
// Returns a synthesized body: {"data": [... all items ...]}
func fetchCursor(
	base transport.HTTPRequest,
	baseURL, baseQuery string,
	pg manifest.Pagination,
	flags Flags,
) ([]byte, error) {
	var allItems []any
	cursor := ""

	for page := 0; page < maxPages; page++ {
		url := buildPageURL(baseURL, baseQuery, pg.Param, cursor)
		base.URL = url

		respBody, err := transport.DoHTTP(base)
		if err != nil {
			return nil, err
		}

		items, err := extractData(respBody, pg.Data)
		if err != nil {
			return nil, fmt.Errorf("cursor page %d: extract data: %w", page+1, err)
		}
		allItems = append(allItems, items...)

		if flags.Limit > 0 && len(allItems) >= flags.Limit {
			allItems = allItems[:flags.Limit]
			break
		}

		next, err := extractScalar(respBody, pg.Next)
		if err != nil {
			return nil, fmt.Errorf("cursor page %d: extract next cursor: %w", page+1, err)
		}
		if next == "" {
			break
		}
		cursor = next
	}

	return synthesizeDataBody(allItems, pg.Data)
}

// fetchPageNumber implements page-number pagination (1-based).
// Increments the page param by 1 each call.
// Stops when a page returns fewer items than the previous page (or empty).
func fetchPageNumber(
	base transport.HTTPRequest,
	baseURL, baseQuery string,
	pg manifest.Pagination,
	flags Flags,
) ([]byte, error) {
	var allItems []any
	pageNum := 1
	var prevLen int

	for page := 0; page < maxPages; page++ {
		url := buildPageURL(baseURL, baseQuery, pg.Param, strconv.Itoa(pageNum))
		base.URL = url

		respBody, err := transport.DoHTTP(base)
		if err != nil {
			return nil, err
		}

		items, err := extractData(respBody, pg.Data)
		if err != nil {
			return nil, fmt.Errorf("page-number page %d: extract data: %w", pageNum, err)
		}

		if len(items) == 0 {
			break
		}
		if page > 0 && len(items) < prevLen {
			allItems = append(allItems, items...)
			break
		}

		allItems = append(allItems, items...)
		prevLen = len(items)

		if flags.Limit > 0 && len(allItems) >= flags.Limit {
			allItems = allItems[:flags.Limit]
			break
		}

		pageNum++
	}

	return synthesizeDataBody(allItems, pg.Data)
}

// fetchPageUntilShort implements page-until-short pagination.
// Like page-number but the stop condition is "page is shorter than the first full page".
func fetchPageUntilShort(
	base transport.HTTPRequest,
	baseURL, baseQuery string,
	pg manifest.Pagination,
	flags Flags,
) ([]byte, error) {
	var allItems []any
	pageNum := 1
	fullPageLen := -1

	for page := 0; page < maxPages; page++ {
		url := buildPageURL(baseURL, baseQuery, pg.Param, strconv.Itoa(pageNum))
		base.URL = url

		respBody, err := transport.DoHTTP(base)
		if err != nil {
			return nil, err
		}

		items, err := extractData(respBody, pg.Data)
		if err != nil {
			return nil, fmt.Errorf("page-until-short page %d: extract data: %w", pageNum, err)
		}

		if len(items) == 0 {
			break
		}

		// Record the first full page's length as the reference.
		if fullPageLen < 0 {
			fullPageLen = len(items)
		}

		allItems = append(allItems, items...)

		if flags.Limit > 0 && len(allItems) >= flags.Limit {
			allItems = allItems[:flags.Limit]
			break
		}

		// A short page means this is the last page.
		if len(items) < fullPageLen {
			break
		}

		pageNum++
	}

	return synthesizeDataBody(allItems, pg.Data)
}

// buildPageURL appends paramName=value to the base query string (if non-empty).
// If paramName or value is empty, returns baseURL+?+baseQuery unchanged.
func buildPageURL(baseURL, baseQuery, paramName, value string) string {
	parts := []string{}
	if baseQuery != "" {
		parts = append(parts, baseQuery)
	}
	if paramName != "" && value != "" {
		parts = append(parts, paramName+"="+value)
	}
	if len(parts) == 0 {
		return baseURL
	}
	return baseURL + "?" + strings.Join(parts, "&")
}

// extractData runs the jq path against the response body and returns the items
// as []any. If dataPath is empty, the entire decoded body is treated as the array.
func extractData(body []byte, dataPath string) ([]any, error) {
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	filter := "."
	if dataPath != "" {
		filter = dataPath
	}

	q, err := gojq.Parse(filter)
	if err != nil {
		return nil, fmt.Errorf("parse data filter %q: %w", filter, err)
	}

	iter := q.Run(parsed)
	v, ok := iter.Next()
	if !ok {
		return nil, nil
	}
	if errV, ok := v.(error); ok {
		return nil, errV
	}

	switch tv := v.(type) {
	case []any:
		return tv, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("data path %q: expected array, got %T", filter, v)
	}
}

// extractScalar runs the jq path and returns the first result as a string.
// Returns "" if the result is null/nil/absent.
func extractScalar(body []byte, jqPath string) (string, error) {
	if jqPath == "" {
		return "", nil
	}

	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	q, err := gojq.Parse(jqPath)
	if err != nil {
		return "", fmt.Errorf("parse next-cursor filter %q: %w", jqPath, err)
	}

	iter := q.Run(parsed)
	v, ok := iter.Next()
	if !ok {
		return "", nil
	}
	if errV, ok := v.(error); ok {
		return "", errV
	}

	switch tv := v.(type) {
	case nil:
		return "", nil
	case string:
		return tv, nil
	default:
		return fmt.Sprintf("%v", tv), nil
	}
}

// synthesizeDataBody builds {"data": allItems} using the same key as the dataPath
// so the command's existing output filter (e.g. "(.data // .) | map(...)") still works.
// If dataPath is empty or ".", the key defaults to "data".
func synthesizeDataBody(items []any, dataPath string) ([]byte, error) {
	// Derive the key name from the jq path (e.g. ".data" → "data", ".results" → "results").
	key := "data"
	if dataPath != "" && dataPath != "." {
		// Strip leading "." for simple single-key paths like ".data" or ".results".
		candidate := strings.TrimPrefix(dataPath, ".")
		if candidate != "" && !strings.ContainsAny(candidate, " |.[]{}") {
			key = candidate
		}
	}

	out := map[string]any{key: items}
	return json.Marshal(out)
}

// resolvedEndpoint is the flattened connection target for a command.
type resolvedEndpoint struct {
	BaseURL     string
	Auth        *manifest.Auth
	TLSInsecure bool
	Codec       manifest.Codec
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
	return resolvedEndpoint{
		BaseURL:     ep.BaseURL,
		Auth:        ep.Auth,
		TLSInsecure: ep.TLSInsecure,
		Codec:       ep.Codec,
	}, nil
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
	// Pagination: fixed-query appends its static string; other styles are handled
	// in the pagination loop (executePaginated), not here.
	if cmd.Pagination.Style == "fixed-query" && cmd.Pagination.Query != "" {
		parts = append(parts, strings.TrimPrefix(cmd.Pagination.Query, "?"))
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
	case "oauth2-client-credentials":
		out["Authorization"] = "Bearer <redacted>"
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
