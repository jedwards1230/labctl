// Package secret resolves credential references to values at call time. Refs
// live in manifests; values never do. Resolution order per secret: an env
// override (skips the resolver, for ephemeral devcontainers/CI), else the
// configured external tool (default `op read {ref}`). Resolved values are cached
// for the process lifetime and never written to disk.
package secret

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jedwards1230/labctl/internal/manifest"
)

// Runner executes a resolver argv and returns its trimmed stdout. Injectable so
// tests can stub `op` without a real 1Password session.
type Runner func(argv []string) (string, error)

// Resolver resolves a service's secrets. One per service invocation.
type Resolver struct {
	spec    manifest.SecretResolver
	secrets map[string]manifest.Secret
	prefix  string
	getenv  func(string) string
	run     Runner
	cache   map[string]string

	// extraEnv is the resolver subprocess's full environment (os.Environ() plus
	// the manifest's secret.env injections), built lazily and memoized so files
	// are never read on a dry-run that resolves no secrets.
	extraEnv  []string
	extraOnce bool
	extraErr  error
}

// New builds a resolver for a service. envPrefix enables <PREFIX>_<FIELD>
// overrides; runner defaults to exec when nil.
func New(spec manifest.SecretResolver, secrets map[string]manifest.Secret, envPrefix string, runner Runner) *Resolver {
	if len(spec.Command) == 0 {
		spec.Command = append([]string(nil), manifest.DefaultResolverCommand...)
	}
	r := &Resolver{
		spec:    spec,
		secrets: secrets,
		prefix:  envPrefix,
		getenv:  os.Getenv,
		run:     runner,
		cache:   map[string]string{},
	}
	if r.run == nil {
		// Default runner: exec with the resolver-subprocess env injected. The
		// closure captures r so it can lazily build the env at first resolve —
		// never on a dry-run that resolves nothing.
		r.run = func(argv []string) (string, error) {
			env, err := r.resolverEnv()
			if err != nil {
				return "", err
			}
			return execRunnerEnv(argv, env)
		}
	}
	return r
}

// resolverEnv returns the full environment for the resolver subprocess,
// memoized. It is os.Environ() with the manifest's secret.env injections
// appended (so injected vars win on a duplicate name). Files are read here, at
// first use — not in New.
func (r *Resolver) resolverEnv() ([]string, error) {
	if r.extraOnce {
		return r.extraEnv, r.extraErr
	}
	r.extraOnce = true
	extra, err := buildResolverEnv(r.spec.Env, r.getenv, os.ReadFile)
	if err != nil {
		r.extraErr = err
		return nil, err
	}
	if len(extra) == 0 {
		// No injections: leave cmd.Env unset so the subprocess inherits ours.
		r.extraEnv = nil
		return nil, nil
	}
	// GOTCHA: setting cmd.Env REPLACES the whole environment, so we must start
	// from os.Environ() and append — otherwise the resolver loses PATH/HOME.
	r.extraEnv = append(os.Environ(), extra...)
	return r.extraEnv, nil
}

// withGetenv overrides the env lookup (tests).
func (r *Resolver) withGetenv(f func(string) string) *Resolver { r.getenv = f; return r }

// Secret resolves the named secret, caching the result.
func (r *Resolver) Secret(name string) (string, error) {
	if v, ok := r.cache[name]; ok {
		return v, nil
	}
	spec, ok := r.secrets[name]
	if !ok {
		return "", fmt.Errorf("secret %q is not declared in this manifest", name)
	}

	// 1. Env override (explicit per-secret env, then <PREFIX>_<NAME>).
	if r.spec.EnvOverride || spec.Env != "" {
		if v := r.envOverride(name, spec); v != "" {
			r.cache[name] = v
			return v, nil
		}
	}

	// 2. External resolver.
	v, err := r.resolve(spec)
	if err != nil {
		return "", fmt.Errorf("resolve secret %q: %w", name, err)
	}
	if v == "" {
		return "", fmt.Errorf("secret %q resolved empty (ref %q)", name, spec.Ref)
	}
	r.cache[name] = v
	return v, nil
}

func (r *Resolver) envOverride(name string, spec manifest.Secret) string {
	if spec.Env != "" {
		if v := r.getenv(spec.Env); v != "" {
			return v
		}
	}
	if r.prefix != "" {
		key := strings.ToUpper(r.prefix + "_" + name)
		if v := r.getenv(key); v != "" {
			return v
		}
	}
	return ""
}

func (r *Resolver) resolve(spec manifest.Secret) (string, error) {
	idiom := spec.Idiom
	if idiom == "" {
		idiom = "read"
	}
	switch idiom {
	case "read":
		return r.resolveRead(spec)
	case "item-get", "item-json":
		return r.resolveItem(spec, idiom)
	default:
		return "", fmt.Errorf("unknown idiom %q", idiom)
	}
}

// resolveRead runs the configured resolver command with {ref} substituted. With
// a fields fallback, it tries each candidate (replacing the ref's final segment)
// until one returns non-empty.
func (r *Resolver) resolveRead(spec manifest.Secret) (string, error) {
	refs := []string{spec.Ref}
	if len(spec.Fields) > 0 {
		refs = refs[:0]
		for _, f := range spec.Fields {
			refs = append(refs, replaceLastSegment(spec.Ref, f))
		}
	}
	var lastErr error
	for _, ref := range refs {
		argv := substituteRef(r.spec.Command, ref)
		out, err := r.run(argv)
		if err != nil {
			lastErr = err
			continue
		}
		if out != "" {
			return out, nil
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", nil
}

// resolveItem builds an op-specific `item get` argv. The ref is parsed as
// op://<vault>/<item>/<field>.
func (r *Resolver) resolveItem(spec manifest.Secret, idiom string) (string, error) {
	vault, item, field, err := parseOpRef(spec.Ref)
	if err != nil {
		return "", err
	}
	var argv []string
	switch idiom {
	case "item-get":
		argv = []string{"op", "item", "get", item, "--vault", vault, "--field", field, "--reveal"}
	case "item-json":
		// Resolve the whole item; field selection is left to the caller's filter.
		argv = []string{"op", "item", "get", item, "--vault", vault, "--format", "json", "--reveal"}
		_ = field
	}
	return r.run(argv)
}

func substituteRef(command []string, ref string) []string {
	out := make([]string, len(command))
	for i, a := range command {
		out[i] = strings.ReplaceAll(a, "{ref}", ref)
	}
	return out
}

func replaceLastSegment(ref, field string) string {
	i := strings.LastIndexByte(ref, '/')
	if i < 0 {
		return ref
	}
	return ref[:i+1] + field
}

func parseOpRef(ref string) (vault, item, field string, err error) {
	trimmed := strings.TrimPrefix(ref, "op://")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 3 {
		return "", "", "", fmt.Errorf("ref %q is not op://vault/item/field", ref)
	}
	return parts[0], parts[1], parts[2], nil
}

// execRunnerEnv runs the resolver argv. When env is non-nil it REPLACES the
// subprocess environment wholesale (callers must pass a complete os.Environ()
// plus their additions); a nil env inherits labctl's environment.
func execRunnerEnv(argv, env []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty resolver command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stderr = os.Stderr // let op print its own diagnostics (session expired, etc.)
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", argv[0], err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// buildResolverEnv builds the NAME=value entries injected into the resolver
// subprocess from the manifest's secret.env map. It is pure (no globals): file
// reads and env lookups go through the injected funcs. Exactly one source per
// entry is expected — config validation enforces that earlier.
func buildResolverEnv(spec map[string]manifest.SecretEnvSource, getenv func(string) string,
	readFile func(string) ([]byte, error)) ([]string, error) {
	if len(spec) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(spec))
	for name, src := range spec {
		var val string
		switch {
		case src.File != "":
			b, err := readFile(expandPath(src.File, getenv))
			if err != nil {
				return nil, fmt.Errorf("secret.env %q: read file: %w", name, err)
			}
			val = strings.TrimSpace(string(b))
		case src.Value != "":
			val = src.Value
		case src.Env != "":
			val = getenv(src.Env)
		default:
			return nil, fmt.Errorf("secret.env %q: no source set", name)
		}
		out = append(out, name+"="+val)
	}
	return out, nil
}

// expandPath expands a leading ~ (home dir) and any $VAR references using getenv.
func expandPath(p string, getenv func(string) string) string {
	if strings.HasPrefix(p, "~/") || p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			p = home + strings.TrimPrefix(p, "~")
		}
	}
	return os.Expand(p, getenv)
}
