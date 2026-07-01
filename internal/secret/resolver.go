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

// New builds a resolver for a service. ctx carries cancellation/deadline into the
// provider's subprocess (a nil ctx defaults to context.Background()); cfg supplies
// the (normalized) secrets providers; envPrefix enables <PREFIX>_<NAME> overrides;
// runner stubs `op` in tests (nil = real exec). Normalizing here means hand-built
// Configs (tests, the legacy `secret:` block) work without going through Load.
func New(ctx context.Context, cfg manifest.Config, secrets map[string]manifest.Secret, envPrefix string, runner Runner) *Resolver {
	if ctx == nil {
		ctx = context.Background()
	}
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
		ctx:         ctx,
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
		// An undeclared-secret reference is a config mistake (the manifest names a
		// secret it never declares) → ConfigError → exit 2.
		return "", &ConfigError{Err: fmt.Errorf("secret %q is not declared in this manifest", name)}
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
		// A provider/`op` failure (notably an expired op session) is a credential
		// failure → AuthError → exit 3, matching the auth-strategy path. Without
		// this, the same failure routed through body/query/path/header/params
		// would exit 1 instead of 3.
		return "", &AuthError{Err: fmt.Errorf("resolve secret %q: %w", name, err)}
	}
	if v == "" {
		return "", &AuthError{Err: fmt.Errorf("secret %q resolved empty (ref %q)", name, spec.Ref)}
	}
	r.cache[name] = v
	return v, nil
}

// ResolvedSecretBinaries returns a diagnostic scheme → resolved-path (or
// "unresolved: …") map for every registered secrets provider that exposes a
// resolvable external binary (currently "op"). Pure lookup — no invocation,
// no secret resolution — intended for --dry-run/--verbose audit output so an
// operator can see exactly which binary on $PATH would be trusted with
// credentials before a real command ever runs.
func (r *Resolver) ResolvedSecretBinaries() map[string]string {
	return r.registry.ResolvedBinaries()
}

// ResolvedValues returns a snapshot of the non-empty secret values currently in
// the resolver's cache. Order is unspecified — a caller that needs determinism
// (e.g. NewScrubber) sorts the result. Used to build a value-based scrubber so
// resolved credentials are stripped from diagnostics at the transport layer.
func (r *Resolver) ResolvedValues() []string {
	out := make([]string, 0, len(r.cache))
	for _, v := range r.cache {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
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
