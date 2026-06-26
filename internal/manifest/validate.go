package manifest

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/itchyny/gojq"
)

var validTransports = map[string]bool{
	"http":       true,
	"jsonrpc-ws": true,
}

var validStrategies = map[string]bool{
	"":                          true, // inherits/none
	"none":                      true,
	"header-key":                true,
	"bearer":                    true,
	"basic":                     true,
	"oauth2-client-credentials": true,
	"ws-login":                  true,
	"login-flow":                true,
	"external-tool":             true,
}

var validOutputModes = map[string]bool{
	"":       true,
	"json":   true,
	"raw":    true,
	"scalar": true,
	"table":  true, // accepted in schema; render deferred
}

// validPaginationStyles is the exhaustive set of styles the engine can execute.
// Any other value is a typo that would silently return only page 1.
var validPaginationStyles = map[string]bool{
	"":                 true, // same as none
	"none":             true,
	"cursor":           true,
	"page-number":      true,
	"page-until-short": true,
	"fixed-query":      true,
}

// Validate checks a service manifest for internal consistency. It does not touch
// the network or resolve secrets — purely structural (used by `labctl lint`). Any
// failure is wrapped in *ConfigError so callers classify it to the usage exit
// code (2), regardless of entry point.
//
// Note: when spec: is set, this function validates the field value's syntax but
// does NOT load the document (that happens at load time via InferredCommands). A
// manifest with an unreachable spec: passes lint but fails at load/use time.
func Validate(s *Service) error {
	if err := validate(s); err != nil {
		return &ConfigError{Err: err}
	}
	return nil
}

// validate is the unwrapped structural check. Validate wraps its result in
// *ConfigError; keeping the body separate avoids double-wrapping at the many
// internal fmt.Errorf call sites.
func validate(s *Service) error {
	if s.BaseURL == "" && len(s.Endpoints) == 0 {
		return fmt.Errorf("service must set base_url or at least one endpoint")
	}
	if err := validateSpec(s); err != nil {
		return err
	}
	if !validTransports[transportOf(s.Transport)] {
		return fmt.Errorf("unknown transport %q (want http|jsonrpc-ws)", s.Transport)
	}
	if !validStrategies[s.Auth.Strategy] {
		return fmt.Errorf("unknown auth strategy %q", s.Auth.Strategy)
	}
	if err := validateAuth(s.Auth, s.Secrets); err != nil {
		return err
	}
	if !validOutputModes[s.Output.Mode] {
		return fmt.Errorf("unknown output mode %q (want json|raw|scalar)", s.Output.Mode)
	}
	if !validPaginationStyles[s.Pagination.Style] {
		return fmt.Errorf("unknown pagination style %q (want none|cursor|page-number|page-until-short|fixed-query)", s.Pagination.Style)
	}
	for name, sec := range s.Secrets {
		if sec.Ref == "" && sec.Env == "" {
			return fmt.Errorf("secret %q must set ref or env", name)
		}
		if sec.Idiom != "" && sec.Idiom != "read" && sec.Idiom != "item-get" && sec.Idiom != "item-json" {
			return fmt.Errorf("secret %q: unknown idiom %q (want read|item-get|item-json)", name, sec.Idiom)
		}
	}
	for id, c := range s.Commands {
		if err := validateCommand(id, c, s); err != nil {
			return err
		}
	}
	for name, ep := range s.Endpoints {
		if ep.BaseURL == "" {
			return fmt.Errorf("endpoint %q must set base_url", name)
		}
	}
	return nil
}

func validateCommand(id string, c Command, s *Service) error {
	if len(c.Steps) > 0 {
		return validateSteps(id, c, s)
	}
	if c.Method == "" {
		return fmt.Errorf("command %q must set method", id)
	}
	if transportOf(s.Transport) == "http" && c.Path == "" && c.Endpoint == "" {
		return fmt.Errorf("command %q must set path", id)
	}
	if c.Endpoint != "" {
		if _, ok := s.Endpoints[c.Endpoint]; !ok {
			return fmt.Errorf("command %q references unknown endpoint %q", id, c.Endpoint)
		}
	}
	if !validPaginationStyles[c.Pagination.Style] {
		return fmt.Errorf("command %q: unknown pagination style %q (want none|cursor|page-number|page-until-short|fixed-query)", id, c.Pagination.Style)
	}
	// A jsonrpc-ws command's params must be a JSON array (template-tolerant).
	if transportOf(s.Transport) == "jsonrpc-ws" && c.Params != "" {
		if err := validateJSONRPCParams(id, c.Params); err != nil {
			return err
		}
	}
	return nil
}

// templateToken matches a {…} template segment so JSON-array shape checks can
// tolerate non-JSON template grammar like [{arg.0}].
var templateToken = regexp.MustCompile(`\{[^}]*\}`)

// validateJSONRPCParams checks that a jsonrpc-ws command's params is a JSON
// array. Template tokens are replaced with a placeholder first, so [{arg.0}]
// (valid per the template grammar but not valid JSON) passes, while a non-array
// like "{not an array}" fails.
func validateJSONRPCParams(id, params string) error {
	normalized := templateToken.ReplaceAllString(params, "0")
	var arr []any
	if err := json.Unmarshal([]byte(normalized), &arr); err != nil {
		return &ConfigError{Err: fmt.Errorf("command %q: params must be a JSON array: %w", id, err)}
	}
	return nil
}

// validateSteps validates each step of a composed (pipeline) command.
func validateSteps(id string, c Command, s *Service) error {
	for i, step := range c.Steps {
		stepID := step.ID
		if stepID == "" {
			stepID = fmt.Sprintf("step[%d]", i)
		}
		if err := validateStep(id, stepID, step, s); err != nil {
			return err
		}
	}
	return nil
}

// validateStep validates one pipeline step (and recursively its on_error step):
// a named endpoint must exist, the step must target a path or endpoint, and every
// jq expression (extract/when/body_transform) must parse.
func validateStep(cmdID, stepID string, step Step, s *Service) error {
	if step.Endpoint != "" {
		if _, ok := s.Endpoints[step.Endpoint]; !ok {
			return &ConfigError{Err: fmt.Errorf("command %q %s: references unknown endpoint %q", cmdID, stepID, step.Endpoint)}
		}
	}
	if step.Path == "" && step.Endpoint == "" {
		return &ConfigError{Err: fmt.Errorf("command %q %s: must set path or endpoint", cmdID, stepID)}
	}
	for varName, expr := range step.Extract {
		if _, err := gojq.Parse(expr); err != nil {
			return &ConfigError{Err: fmt.Errorf("command %q %s: extract %q: invalid jq %q: %w", cmdID, stepID, varName, expr, err)}
		}
	}
	if step.When != "" {
		if _, err := gojq.Parse(step.When); err != nil {
			return &ConfigError{Err: fmt.Errorf("command %q %s: when: invalid jq %q: %w", cmdID, stepID, step.When, err)}
		}
	}
	if step.BodyTransform != "" {
		if _, err := gojq.Parse(step.BodyTransform); err != nil {
			return &ConfigError{Err: fmt.Errorf("command %q %s: body_transform: invalid jq %q: %w", cmdID, stepID, step.BodyTransform, err)}
		}
	}
	if step.OnError != nil {
		if err := validateStep(cmdID, stepID+".on_error", *step.OnError, s); err != nil {
			return err
		}
	}
	return nil
}

func validateAuth(a Auth, secrets map[string]Secret) error {
	switch a.Strategy {
	case "header-key":
		if a.Header == "" {
			return fmt.Errorf("auth header-key requires a header name")
		}
		if a.Value == "" {
			return fmt.Errorf("auth header-key requires a value template")
		}
	case "bearer":
		if a.Value == "" {
			return fmt.Errorf("auth bearer requires a value template")
		}
	case "basic":
		if a.Username == "" || a.Password == "" {
			return fmt.Errorf("auth basic requires username and password templates")
		}
	case "oauth2-client-credentials":
		if a.Value == "" {
			return fmt.Errorf("auth oauth2-client-credentials requires value (token URL)")
		}
		if a.Username == "" || a.Password == "" {
			return fmt.Errorf("auth oauth2-client-credentials requires username (client_id) and password (client_secret) templates")
		}
	}
	// Verify {secret.X} references resolve to a declared secret.
	for _, tmpl := range []string{a.Value, a.Username, a.Password} {
		for _, ref := range secretRefs(tmpl) {
			if _, ok := secrets[ref]; !ok {
				return fmt.Errorf("auth references undeclared secret %q", ref)
			}
		}
	}
	return nil
}

// validateSpec checks that spec: (if set) is a non-empty string and that any
// SpecFilter patterns are non-empty strings. It does NOT load the document.
func validateSpec(s *Service) error {
	if s.Spec == "" {
		if len(s.SpecFilter.Include) > 0 || len(s.SpecFilter.Exclude) > 0 {
			return fmt.Errorf("spec_filter requires spec to be set")
		}
		return nil
	}
	// Must be either an http(s):// URL or a relative/absolute file path (non-empty).
	if strings.TrimSpace(s.Spec) == "" {
		return fmt.Errorf("spec must be a non-empty file path or http(s):// URL")
	}
	for i, p := range s.SpecFilter.Include {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("spec_filter.include[%d] must be a non-empty string", i)
		}
	}
	for i, p := range s.SpecFilter.Exclude {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("spec_filter.exclude[%d] must be a non-empty string", i)
		}
	}
	return nil
}

// validSecretSchemes is the set of URI schemes a secrets provider can serve.
var validSecretSchemes = map[string]bool{
	"op": true,
}

// ConfigError marks a global-config validation problem (exit 2). It mirrors
// secret.ConfigError on the resolve path so that load-time config-validation
// failures classify to the same usage exit code, regardless of entry point.
type ConfigError struct{ Err error }

func (e *ConfigError) Error() string { return e.Err.Error() }
func (e *ConfigError) Unwrap() error { return e.Err }

// DecodeError marks a spec/response decode failure (exit 6). It mirrors the
// transport-layer decode classification so that load-time decode failures (a
// non-200 spec fetch, an unparseable OpenAPI document) classify to the same exit
// code as a runtime decode error.
type DecodeError struct{ Err error }

func (e *DecodeError) Error() string { return e.Err.Error() }
func (e *DecodeError) Unwrap() error { return e.Err }

// ValidateConfig checks the global config's secrets providers for consistency:
// each provider's scheme must be known, and a service_account_token (if set)
// must name exactly one of file|value|env. Structural only — it runs no tools
// and reads no token files. Safe to call on a normalized or raw Config.
func ValidateConfig(c *Config) error {
	for name, p := range c.Secrets.Providers {
		scheme := p.Scheme
		if scheme == "" {
			scheme = name
		}
		if !validSecretSchemes[scheme] {
			return &ConfigError{Err: fmt.Errorf("secrets provider %q: unknown scheme %q (want op)", name, scheme)}
		}
		if tok := p.Auth.ServiceAccountToken; tok != nil {
			if err := validateSecretSource(*tok); err != nil {
				return &ConfigError{Err: fmt.Errorf("secrets provider %q: service_account_token: %w", name, err)}
			}
		}
	}
	return nil
}

func validateSecretSource(s SecretSource) error {
	n := 0
	if s.File != "" {
		n++
	}
	if s.Value != "" {
		n++
	}
	if s.Env != "" {
		n++
	}
	if n != 1 {
		return fmt.Errorf("set exactly one of file|value|env (found %d)", n)
	}
	return nil
}

func transportOf(t string) string {
	if t == "" {
		return "http"
	}
	return t
}

// secretRefs extracts the X from each {secret.X} token in a template.
func secretRefs(tmpl string) []string {
	var out []string
	rest := tmpl
	for {
		i := strings.Index(rest, "{secret.")
		if i < 0 {
			break
		}
		rest = rest[i+len("{secret."):]
		j := strings.IndexByte(rest, '}')
		if j < 0 {
			break
		}
		out = append(out, rest[:j])
		rest = rest[j+1:]
	}
	return out
}
