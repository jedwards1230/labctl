package secret

// This file defines the secrets-provider seam. Resolution dispatches on a ref's
// URI scheme (op://… → the op provider), so the resolver and engine stay
// scheme-agnostic.
//
// To add a provider (e.g. aws://, vault://):
//
//  1. Implement Provider (a new Scheme() + Resolve()) in its own file.
//  2. Add a `secrets.providers.<name>` block to config.yaml.
//  3. Register its constructor in NewRegistry's scheme switch.
//
// No engine or cli changes are required — dispatch is by URI scheme.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jedwards1230/labctl/internal/manifest"
)

// Provider resolves secret references for a single URI scheme.
type Provider interface {
	// Scheme is the URI scheme this provider handles (e.g. "op" for op://…).
	Scheme() string
	// Resolve turns a reference into its secret value.
	Resolve(ctx context.Context, ref Ref) (string, error)
}

// Ref is a scheme-agnostic secret reference. Fields/Idiom carry the op-specific
// item/field selection so the dispatcher need not understand any one scheme.
type Ref struct {
	URI    string   // full reference, e.g. op://vault/item/field
	Fields []string // ordered field fallback (resolve first non-empty)
	Idiom  string   // read (default) | item-get | item-json
}

// Registry routes a ref to the provider registered for its scheme.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry builds providers from a normalized SecretsConfig. Pure and
// side-effect-free: it reads no files, runs no tools, and resolves no tokens.
func NewRegistry(cfg manifest.SecretsConfig, runner Runner) *Registry {
	reg := &Registry{providers: map[string]Provider{}}
	for name, pc := range cfg.Providers {
		scheme := pc.Scheme
		if scheme == "" {
			scheme = name
		}
		switch scheme {
		case "op", "onepassword":
			p := newOnePassword(pc, runner)
			reg.providers[p.Scheme()] = p
		}
	}
	return reg
}

// For returns the provider registered for scheme, if any.
func (r *Registry) For(scheme string) (Provider, bool) {
	p, ok := r.providers[scheme]
	return p, ok
}

// binaryResolver is an optional capability a Provider MAY implement to expose
// the resolved absolute path of the external binary it shells out to (e.g.
// "op" for OnePassword), purely for --dry-run/--verbose audit visibility. It
// performs no invocation, so a resolution failure is not an execution
// failure — dry-run visibility only, never a gate.
type binaryResolver interface {
	ResolvedBinary() (string, error)
}

// ResolvedBinaries returns a diagnostic scheme → description map for every
// registered provider that implements binaryResolver. A successful lookup
// maps to the resolved absolute path; a failed one maps to a human-readable
// "unresolved: <reason>" note rather than being silently omitted, so an
// operator auditing --dry-run output sees the failure too. Providers that
// don't implement binaryResolver are omitted (nothing to show).
func (r *Registry) ResolvedBinaries() map[string]string {
	out := map[string]string{}
	for scheme, p := range r.providers {
		br, ok := p.(binaryResolver)
		if !ok {
			continue
		}
		path, err := br.ResolvedBinary()
		if err != nil {
			out[scheme] = fmt.Sprintf("unresolved: %v", err)
			continue
		}
		out[scheme] = path
	}
	return out
}

// schemeOf returns the URI scheme (substring before "://"), or "" if absent.
func schemeOf(ref string) string {
	i := strings.Index(ref, "://")
	if i < 0 {
		return ""
	}
	return ref[:i]
}

// AuthError marks a credential/authentication failure (exit 3).
type AuthError struct{ Err error }

func (e *AuthError) Error() string { return e.Err.Error() }
func (e *AuthError) Unwrap() error { return e.Err }

// ConfigError marks a configuration/usage problem (exit 2).
type ConfigError struct{ Err error }

func (e *ConfigError) Error() string { return e.Err.Error() }
func (e *ConfigError) Unwrap() error { return e.Err }
