package manifest

import (
	"fmt"
	"strings"
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

// Validate checks a service manifest for internal consistency. It does not touch
// the network or resolve secrets — purely structural (used by `labctl lint`).
func Validate(s *Service) error {
	if s.BaseURL == "" && len(s.Endpoints) == 0 {
		return fmt.Errorf("service must set base_url or at least one endpoint")
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
		return nil // composed command — step shape validated at execution (Phase 3)
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
