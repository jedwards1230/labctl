// Package template expands the small {token} grammar used in manifests for
// requests: {secret.X} resolves a credential, {env.X} reads an env var, {arg.N}
// and {argN} read a positional arg, and a bare {name} reads a service var.
//
// This is deliberately NOT a programming language — it is one-level substitution
// only (the plan's "one templating grammar for requests"; jq is the separate
// data grammar). Unknown tokens are an error so typos surface loudly.
package template

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Resolver lazily resolves a named secret to its value. Implemented by the
// secret package; injected so template stays dependency-free.
type Resolver interface {
	Secret(name string) (string, error)
}

// Env is the substitution context for one command invocation.
type Env struct {
	Vars    map[string]string   // service vars (host, …)
	Args    []string            // positional CLI args
	Secrets Resolver            // may be nil if the template uses no secrets
	Getenv  func(string) string // env lookup; defaults to os.Getenv when nil
}

// tokenRe matches a well-formed template token: an identifier with optional
// dotted/dashed segments (secret.api_key, env.HOST, arg.0, host). JSON object
// braces never match (they contain quotes, colons, or nested braces), so a JSON
// body passes through untouched without escaping.
var tokenRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// Expand replaces every {token} in s where the content is a well-formed token.
// A "{" that does not open a valid token (e.g. a JSON brace) is emitted
// literally. It returns an error on an unknown well-formed token or a failed
// secret resolution.
func (e Env) Expand(s string) (string, error) {
	getenv := e.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c != '{' {
			b.WriteByte(c)
			i++
			continue
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			// No closing brace at all — rest is literal.
			b.WriteString(s[i:])
			break
		}
		token := s[i+1 : i+j]
		if !tokenRe.MatchString(token) {
			// Not a template token (JSON brace, etc.) — emit "{" literally and
			// rescan from the next char so nested braces are handled.
			b.WriteByte('{')
			i++
			continue
		}
		val, err := e.resolveToken(token, getenv)
		if err != nil {
			return "", err
		}
		b.WriteString(val)
		i += j + 1
	}
	return b.String(), nil
}

func (e Env) resolveToken(token string, getenv func(string) string) (string, error) {
	switch {
	case strings.HasPrefix(token, "secret."):
		name := token[len("secret."):]
		if e.Secrets == nil {
			return "", fmt.Errorf("template references {secret.%s} but no secret resolver is configured", name)
		}
		return e.Secrets.Secret(name)
	case strings.HasPrefix(token, "env."):
		return getenv(token[len("env."):]), nil
	case strings.HasPrefix(token, "arg."):
		return e.arg(token[len("arg."):])
	case strings.HasPrefix(token, "arg"):
		return e.arg(token[len("arg"):])
	default:
		if v, ok := e.Vars[token]; ok {
			return v, nil
		}
		return "", fmt.Errorf("unknown template token {%s}", token)
	}
}

func (e Env) arg(idx string) (string, error) {
	n, err := strconv.Atoi(idx)
	if err != nil {
		return "", fmt.Errorf("invalid arg index in {arg%s}", idx)
	}
	if n < 0 || n >= len(e.Args) {
		return "", fmt.Errorf("template references {arg%d} but only %d args were given", n, len(e.Args))
	}
	return e.Args[n], nil
}
