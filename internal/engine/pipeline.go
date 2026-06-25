// pipeline.go implements the composed-command (steps:) executor for Phase 3.
//
// Pipeline context (accVars map[string]any):
//   - Seeded from service vars (string → any) at pipeline start
//   - Each step's extract adds varName → jq result (any)
//   - Each step's capture_header adds varName → header value (string)
//   - Template expansion uses accVars merged into Env.Vars (any→string via fmt.Sprintf)
//   - Final output.filter runs jq against accVars directly
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/itchyny/gojq"
	"github.com/jedwards1230/labctl/internal/auth"
	"github.com/jedwards1230/labctl/internal/command"
	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/output"
	"github.com/jedwards1230/labctl/internal/secret"
	"github.com/jedwards1230/labctl/internal/template"
	"github.com/jedwards1230/labctl/internal/transport"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// executePipeline runs cmd.Steps sequentially, accumulating extracted vars,
// then applies the command's output.filter against the final accumulated var map.
func executePipeline(
	ctx context.Context,
	req Request,
	svc *manifest.Service,
	cmd *command.Command,
	baseVars map[string]string,
	baseTmplEnv template.Env,
	stderr io.Writer,
) (*Result, error) {
	span := trace.SpanFromContext(ctx)

	getenv := req.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	// Dry-run: preview the step sequence without resolving secrets or sending.
	if req.Flags.DryRun {
		return dryRunPipeline(svc, cmd)
	}

	// Seed accVars from service vars (string → any).
	// Also add an "env" sub-key with the same service vars so jq filters can
	// reference them as env.host, env.port, etc. (see the sunshine status filter).
	accVars := make(map[string]any, len(baseVars)+1)
	for k, v := range baseVars {
		accVars[k] = v
	}
	envMap := make(map[string]any, len(baseVars))
	for k, v := range baseVars {
		envMap[k] = v
	}
	accVars["env"] = envMap

	for i, step := range cmd.Steps {
		stepID := step.ID
		if stepID == "" {
			stepID = fmt.Sprintf("step[%d]", i)
		}

		span.AddEvent("step", trace.WithAttributes(
			attribute.String("step.id", stepID),
			attribute.String("step.method", step.Method),
		))

		// Build merged vars for this step: accVars (as strings) overlay base service vars.
		mergedVars := accVarsToStrings(baseVars, accVars)
		res := secret.New(req.Config.Secret, svc.Secrets, svc.EnvPrefix, req.Runner)
		stepEnv := template.Env{
			Vars:    mergedVars,
			Args:    baseTmplEnv.Args,
			Secrets: res,
			Getenv:  getenv,
		}

		// When condition: evaluate jq against accVars; skip step if falsy.
		if step.When != "" {
			result, err := pipelineJQFirst(step.When, accVars)
			if err != nil {
				return nil, fmt.Errorf("step %s: when: %w", stepID, err)
			}
			if !pipelineTruthy(result) {
				continue
			}
		}

		// Confirm gate: if the step requires confirmation and --yes/-y was not given, abort.
		if step.Confirm != "" && !req.Flags.Yes {
			return nil, fmt.Errorf("step %q requires confirmation: %s (re-run with --yes/-y)", stepID, step.Confirm)
		}

		// Resolve endpoint for this step.
		ep, err := resolveStepEndpoint(svc, step)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", stepID, err)
		}

		// Execute the step's HTTP request.
		stepErr := runStep(ctx, req, svc, step, ep, stepEnv, accVars, stderr)
		if stepErr != nil {
			if step.OnError != nil {
				// Execute the on_error step then continue the pipeline.
				onErrEnv := template.Env{
					Vars:    accVarsToStrings(baseVars, accVars),
					Args:    baseTmplEnv.Args,
					Secrets: res,
					Getenv:  getenv,
				}
				onErrEp, epErr := resolveStepEndpoint(svc, *step.OnError)
				if epErr == nil {
					_ = runStep(ctx, req, svc, *step.OnError, onErrEp, onErrEnv, accVars, stderr)
				}
				continue
			}
			return nil, fmt.Errorf("step %s: %w", stepID, stepErr)
		}
	}

	// Apply the command's output.filter against the accumulated var map.
	filter := pipelineFirstNonEmpty(cmd.Output.Filter, cmd.Output.DefaultFilter, ".")
	finalVal, err := pipelineJQFirst(filter, accVars)
	if err != nil {
		return nil, fmt.Errorf("pipeline output filter: %w", err)
	}
	body, err := json.Marshal(finalVal)
	if err != nil {
		return nil, fmt.Errorf("pipeline: marshal result: %w", err)
	}

	// The pipeline has already applied the output filter; return the body
	// with an empty filter so the render layer uses "." (pass-through) and
	// does not re-run the same filter against the already-assembled JSON.
	renderedOutput := manifest.Output{Mode: cmd.Output.Mode}
	return &Result{Body: body, Output: renderedOutput}, nil
}

// runStep issues the HTTP request for one pipeline step and updates accVars
// with any extracted vars and captured headers.
func runStep(
	ctx context.Context,
	req Request,
	svc *manifest.Service,
	step manifest.Step,
	ep resolvedEndpoint,
	stepEnv template.Env,
	accVars map[string]any,
	stderr io.Writer,
) error {
	base, err := resolveBaseURL(ep.BaseURL, svc, stepEnv.Vars, stepEnv, stepEnv.Getenv)
	if err != nil {
		return fmt.Errorf("resolve base URL: %w", err)
	}

	path, err := stepEnv.Expand(step.Path)
	if err != nil {
		return fmt.Errorf("expand path: %w", err)
	}
	url := joinURL(base, path)

	if step.Query != "" {
		q, err := stepEnv.Expand(step.Query)
		if err != nil {
			return fmt.Errorf("expand query: %w", err)
		}
		url += "?" + strings.TrimPrefix(q, "?")
	}

	headers, err := expandHeaders(step.Headers, stepEnv)
	if err != nil {
		return err
	}

	// Build request body: body_transform (jq against accVars) or literal body.
	var body []byte
	var contentType string
	if step.BodyTransform != "" {
		transformed, err := pipelineJQFirst(step.BodyTransform, accVars)
		if err != nil {
			return fmt.Errorf("body_transform: %w", err)
		}
		body, err = json.Marshal(transformed)
		if err != nil {
			return fmt.Errorf("body_transform marshal: %w", err)
		}
		contentType = "application/json"
	} else if step.Body != "" {
		expanded, err := stepEnv.Expand(step.Body)
		if err != nil {
			return fmt.Errorf("expand body: %w", err)
		}
		body = []byte(expanded)
		contentType = "application/json"
	}

	authSpec := svc.Auth
	if ep.Auth != nil {
		authSpec = *ep.Auth
	}
	applier := auth.New(authSpec, stepEnv)

	method := step.Method
	if method == "" {
		method = "GET"
	}

	var verbose io.Writer
	if req.Flags.Verbose {
		verbose = stderr
	}

	httpReq := transport.HTTPRequest{
		Ctx:         ctx,
		Method:      method,
		URL:         url,
		Headers:     headers,
		Body:        body,
		ContentType: contentType,
		TLSInsecure: ep.TLSInsecure || svc.TLSInsecure,
		Timeout:     svc.TimeoutDuration(),
		Auth:        applier,
		Verbose:     verbose,
	}

	respBody, respHeaders, err := transport.DoHTTPWithHeaders(httpReq)
	if err != nil {
		return err
	}

	// Decode response for extract: xml or json.
	var decoded any
	codec := step.Decode
	if codec == "" {
		codec = ep.Codec.Response
	}
	if codec == "xml" {
		decoded, err = output.DecodeXML(respBody)
		if err != nil {
			return fmt.Errorf("decode xml: %w", err)
		}
	} else {
		if err := json.Unmarshal(respBody, &decoded); err != nil {
			return fmt.Errorf("decode json: %w", err)
		}
	}

	// Extract: run jq expressions against decoded body → store in accVars.
	for varName, jqExpr := range step.Extract {
		val, err := pipelineJQFirst(jqExpr, decoded)
		if err != nil {
			return fmt.Errorf("extract %q: %w", varName, err)
		}
		accVars[varName] = val
	}

	// CaptureHeader: store response header values in accVars.
	for varName, headerName := range step.CaptureHeader {
		accVars[varName] = respHeaders.Get(headerName)
	}

	return nil
}

// resolveStepEndpoint resolves a step's named endpoint, defaulting to the
// service's base URL and auth when step.Endpoint is empty.
func resolveStepEndpoint(svc *manifest.Service, step manifest.Step) (resolvedEndpoint, error) {
	if step.Endpoint == "" {
		return resolvedEndpoint{BaseURL: svc.BaseURL}, nil
	}
	ep, ok := svc.Endpoints[step.Endpoint]
	if !ok {
		return resolvedEndpoint{}, fmt.Errorf("unknown endpoint %q", step.Endpoint)
	}
	return resolvedEndpoint{
		BaseURL:     ep.BaseURL,
		Auth:        ep.Auth,
		TLSInsecure: ep.TLSInsecure,
		Codec:       ep.Codec,
	}, nil
}

// dryRunPipeline produces a preview of the step sequence without resolving
// secrets or making any network requests.
func dryRunPipeline(svc *manifest.Service, cmd *command.Command) (*Result, error) {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "pipeline: %d step(s)\n", len(cmd.Steps))
	for i, step := range cmd.Steps {
		stepID := step.ID
		if stepID == "" {
			stepID = fmt.Sprintf("step[%d]", i)
		}

		method := step.Method
		if method == "" {
			method = "GET"
		}

		ep, err := resolveStepEndpoint(svc, step)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", stepID, err)
		}
		baseURL := ep.BaseURL
		if baseURL == "" {
			baseURL = svc.BaseURL
		}

		_, _ = fmt.Fprintf(&b, "  step %s: %s %s%s\n", stepID, strings.ToUpper(method), baseURL, step.Path)
		if step.Endpoint != "" {
			_, _ = fmt.Fprintf(&b, "    endpoint: %s\n", step.Endpoint)
		}
		if step.Decode != "" {
			_, _ = fmt.Fprintf(&b, "    decode: %s\n", step.Decode)
		}
		for varName, jqExpr := range step.Extract {
			_, _ = fmt.Fprintf(&b, "    extract %s: %s\n", varName, jqExpr)
		}
		if step.When != "" {
			_, _ = fmt.Fprintf(&b, "    when: %s\n", step.When)
		}
		if step.Confirm != "" {
			_, _ = fmt.Fprintf(&b, "    confirm: %s (requires --yes/-y)\n", step.Confirm)
		}
	}
	filter := pipelineFirstNonEmpty(cmd.Output.Filter, cmd.Output.DefaultFilter)
	if filter != "" {
		_, _ = fmt.Fprintf(&b, "  output filter: %s\n", filter)
	}
	return &Result{DryRunMsg: b.String(), Output: cmd.Output}, nil
}

// accVarsToStrings builds a merged string map: base service vars are the
// foundation; accVars (any→string via fmt.Sprintf) overlay them so later
// steps can reference vars extracted by earlier steps via {varname} templates.
func accVarsToStrings(base map[string]string, accVars map[string]any) map[string]string {
	out := make(map[string]string, len(base)+len(accVars))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range accVars {
		switch s := v.(type) {
		case string:
			out[k] = s
		default:
			out[k] = fmt.Sprintf("%v", v)
		}
	}
	return out
}

// pipelineJQFirst parses and runs a jq expression, returning the first result.
func pipelineJQFirst(expr string, input any) (any, error) {
	q, err := gojq.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("parse jq %q: %w", expr, err)
	}
	iter := q.Run(input)
	v, ok := iter.Next()
	if !ok {
		return nil, nil
	}
	if errV, ok := v.(error); ok {
		return nil, errV
	}
	return v, nil
}

// pipelineTruthy mirrors jq's truthiness: false and null are falsy; everything else truthy.
func pipelineTruthy(v any) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return true
}

// pipelineFirstNonEmpty returns the first non-empty string in the list.
func pipelineFirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
