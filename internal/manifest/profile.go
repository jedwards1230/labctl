package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Profile is the per-user profile.yaml at the config root. It binds portable
// manifests to THIS user's endpoints and credentials and is the SOLE binding
// mechanism — a manifest may not carry a base_url or secret ref itself. It is
// optional only in that a config dir may have services not yet bound; such
// services load but cannot execute until the profile binds them.
//
// A manifest describes WHAT a service is (its commands, auth strategy, secret
// slots); the profile supplies the user-specific WHERE/WHICH (base_url, secret
// refs, per-machine endpoint/var/tls overrides). Precedence at resolution time
// is: env override > profile.
type Profile struct {
	Version  int                       `yaml:"version"`
	Services map[string]ServiceBinding `yaml:"services"`
}

// ServiceBinding is one service's per-user binding. Every field is optional; an
// empty/nil field leaves the manifest's value untouched (per-key merge).
type ServiceBinding struct {
	BaseURL     string                     `yaml:"base_url"`
	TLSInsecure *bool                      `yaml:"tls_insecure"` // pointer so "unset" is distinguishable from false
	Vars        map[string]string          `yaml:"vars"`
	Endpoints   map[string]EndpointBinding `yaml:"endpoints"`
	Secrets     map[string]SecretBinding   `yaml:"secrets"`
}

// EndpointBinding overrides one named endpoint's connection target per machine.
type EndpointBinding struct {
	BaseURL     string `yaml:"base_url"`
	TLSInsecure *bool  `yaml:"tls_insecure"` // pointer so "unset" is distinguishable from false
}

// SecretBinding supplies the user-specific ref/env for a secret slot the
// manifest declares (or, leniently, a slot the manifest omitted).
type SecretBinding struct {
	Ref string `yaml:"ref"`
	Env string `yaml:"env"`
}

// LoadProfile reads <dir>/profile.yaml. A missing file is not an error (returns
// nil, nil) — no profile means no service is bound yet (portable manifests load
// but are incomplete until bound). The profile model is fully closed, so decode
// strictly (KnownFields): a typo'd field is a config error (exit 2) instead of
// being silently dropped. An empty file (io.EOF) is valid and yields an empty
// profile.
func LoadProfile(dir string) (*Profile, error) {
	path := filepath.Join(dir, "profile.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var p Profile
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil && !errors.Is(err, io.EOF) {
		return nil, &ConfigError{Err: fmt.Errorf("parse %s: %w", path, err)}
	}
	if p.Version == 0 {
		p.Version = 1 // cosmetic default; the loader does not branch on version
	}
	return &p, nil
}

// applyProfile overlays a service binding onto an already-loaded and
// mergeDefaults'd service. The profile WINS over the manifest, but only for
// fields the binding actually sets — a binding never clears a manifest value it
// leaves empty/nil (per-key merge). It is pure and total: an orphan binding for
// a service that does not exist is simply never applied by the caller, so there
// is no error path here.
func applyProfile(svc *Service, b ServiceBinding) {
	if b.BaseURL != "" {
		svc.BaseURL = b.BaseURL
	}
	if b.TLSInsecure != nil {
		svc.TLSInsecure = *b.TLSInsecure
	}
	// Vars: per-key merge — manifest keys absent from the binding survive.
	for k, v := range b.Vars {
		if svc.Vars == nil {
			svc.Vars = make(map[string]string, len(b.Vars))
		}
		svc.Vars[k] = v
	}
	// Endpoints: override (or create) each named endpoint's connection target.
	for name, eb := range b.Endpoints {
		if svc.Endpoints == nil {
			svc.Endpoints = make(map[string]Endpoint, len(b.Endpoints))
		}
		ep := svc.Endpoints[name] // zero Endpoint if the manifest didn't declare it
		if eb.BaseURL != "" {
			ep.BaseURL = eb.BaseURL
		}
		if eb.TLSInsecure != nil {
			ep.TLSInsecure = *eb.TLSInsecure
		}
		svc.Endpoints[name] = ep
	}
	// Secrets: bind ref/env on each declared slot — creating the slot if the
	// manifest didn't declare it. Manifest-supplied Fields/Idiom stay intact.
	for name, sb := range b.Secrets {
		if svc.Secrets == nil {
			svc.Secrets = make(map[string]Secret, len(b.Secrets))
		}
		sec := svc.Secrets[name] // zero Secret if the manifest didn't declare it
		if sb.Ref != "" {
			sec.Ref = sb.Ref
		}
		if sb.Env != "" {
			sec.Env = sb.Env
		}
		svc.Secrets[name] = sec
	}
}
