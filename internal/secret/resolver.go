// Package secret resolves credential references to values at call time. Refs
// live in manifests; values never do. Resolution order per secret: an env
// override (skips the provider, for ephemeral devcontainers/CI), else the
// provider registered for the ref's URI scheme (op:// → the 1Password provider,
// default `op read {ref}`). Resolved values are cached for the process lifetime
// and never written to disk.
package secret

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jedwards1230/labctl/internal/manifest"
)

// Runner executes a resolver argv and returns its trimmed stdout. Injectable so
// tests can stub `op` without a real 1Password session.
type Runner func(argv []string) (string, error)

// Resolver resolves a service's secrets. One per service invocation. It dispatches
// each ref to a Provider by URI scheme; the legacy `secret:` block is normalized
// into an op provider, so existing configs resolve unchanged.
type Resolver struct {
	registry    *Registry
	secrets     map[string]manifest.Secret
	prefix      string
	getenv      func(string) string
	envOverride bool
	ctx         context.Context
	cache       map[string]string
}

// New builds a resolver for a service. cfg supplies the (normalized) secrets
// providers; envPrefix enables <PREFIX>_<NAME> overrides; runner stubs `op` in
// tests (nil = real exec). Normalizing here means hand-built Configs (tests, the
// legacy `secret:` block) work without going through Load.
func New(cfg manifest.Config, secrets map[string]manifest.Secret, envPrefix string, runner Runner) *Resolver {
	sc := manifest.NormalizeSecrets(cfg)
	envOverride := false
	if sc.EnvOverride != nil {
		envOverride = *sc.EnvOverride
	}
	return &Resolver{
		registry:    NewRegistry(sc, runner),
		secrets:     secrets,
		prefix:      envPrefix,
		getenv:      os.Getenv,
		envOverride: envOverride,
		ctx:         context.Background(),
		cache:       map[string]string{},
	}
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
	if r.envOverride || spec.Env != "" {
		if v := r.lookupEnv(name, spec); v != "" {
			r.cache[name] = v
			return v, nil
		}
	}

	// 2. Provider resolution, dispatched by the ref's URI scheme.
	scheme := schemeOf(spec.Ref)
	provider, ok := r.registry.For(scheme)
	if !ok {
		return "", &ConfigError{Err: fmt.Errorf("no secrets provider for scheme %q (secret %q ref %q)", scheme, name, spec.Ref)}
	}
	ref := Ref{URI: spec.Ref, Fields: spec.Fields, Idiom: spec.Idiom}
	v, err := provider.Resolve(r.ctx, ref)
	if err != nil {
		return "", fmt.Errorf("resolve secret %q: %w", name, err)
	}
	if v == "" {
		return "", fmt.Errorf("secret %q resolved empty (ref %q)", name, spec.Ref)
	}
	r.cache[name] = v
	return v, nil
}

func (r *Resolver) lookupEnv(name string, spec manifest.Secret) string {
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
