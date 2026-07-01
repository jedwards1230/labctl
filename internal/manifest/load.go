package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jedwards1230/labctl/catalog"
	"gopkg.in/yaml.v3"
)

// DefaultResolverCommand is the secret resolver used when config.yaml omits one.
var DefaultResolverCommand = []string{"op", "read", "{ref}"}

// Origin records where a loaded service came from: the embedded catalog, a local
// services/<name>.yaml, or a local file that overrides an embedded service.
type Origin string

const (
	OriginEmbedded Origin = "embedded" // built-in catalog manifest (top-level catalog package)
	OriginLocal    Origin = "local"    // local services/<name>.yaml with no embedded counterpart
	OriginOverride Origin = "override" // local services/<name>.yaml shadowing an embedded/installed service
)

// originCatalogPrefix marks a provenance value for an installed (named) catalog:
// the dynamic origin "catalog:<name>" records which installed catalog supplied a
// service. It sits between the embedded floor and a local override in precedence.
const originCatalogPrefix = "catalog:"

// catalogOrigin builds the dynamic Origin for an installed catalog of the given name.
func catalogOrigin(name string) Origin { return Origin(originCatalogPrefix + name) }

// IsCatalog reports whether this Origin came from an installed catalog.
func (o Origin) IsCatalog() bool { return strings.HasPrefix(string(o), originCatalogPrefix) }

// Loaded is the merged result of a config dir: the global config plus every
// service manifest, keyed by ADDRESSABLE SELECTOR. A selector is either a bare
// service name (an embedded/local service, or an installed-catalog service with
// exactly one definer) or a qualified "<catalog>:<service>" name (every
// installed-catalog service, always — even a sole definer also gets the
// qualified form). Services come from the embedded catalog, installed catalogs,
// and the local services/ dir, with local overriding installed overriding
// embedded by name.
type Loaded struct {
	Config Config
	// Services is keyed by selector: bare names for embedded/local services and
	// any installed-catalog service with exactly one definer, plus a qualified
	// "<catalog>:<service>" entry for EVERY installed-catalog service. A bare
	// name that more than one installed catalog defines (and no local override
	// claims) is absent here — see Ambiguous.
	Services map[string]*Service
	Origins  map[string]Origin // selector → where it came from
	// Ambiguous maps a bare service name to the sorted list of installed-catalog
	// names that define it, when more than one does and no local override
	// resolves it. The bare name does NOT appear in Services or Origins in this
	// case; only the qualified "<catalog>:<service>" selectors do. Looking up the
	// bare name is a hard error (Lookup) listing the qualified forms — labctl
	// never silently picks one.
	Ambiguous map[string][]string
	Dir       string
	Profile   *Profile // optional per-user profile.yaml (nil when absent)
}

// OriginOf reports where a loaded selector came from (empty for an unknown name).
func (l *Loaded) OriginOf(name string) Origin {
	if l.Origins == nil {
		return ""
	}
	return l.Origins[name]
}

// Lookup resolves a selector — a bare service name or a qualified
// "<catalog>:<service>" name — to its Service. An ambiguous bare name (defined
// by more than one installed catalog, with no local override) is a *ConfigError
// (exit 2) listing the qualified forms to disambiguate with; labctl never
// silently picks one. An unrecognized selector is also a *ConfigError (exit 2).
func (l *Loaded) Lookup(name string) (*Service, error) {
	if cats, ok := l.Ambiguous[name]; ok {
		forms := make([]string, len(cats))
		for i, cat := range cats {
			forms[i] = cat + ":" + name
		}
		return nil, &ConfigError{Err: fmt.Errorf(
			"service %q is defined by %d installed catalogs; qualify it as one of: %s",
			name, len(cats), strings.Join(forms, ", "),
		)}
	}
	if svc, ok := l.Services[name]; ok {
		return svc, nil
	}
	return nil, &ConfigError{Err: fmt.Errorf("unknown service %q", name)}
}

// CanonicalNames returns a sorted, deduplicated selector list with one entry per
// DISTINCT service. A qualified "<catalog>:<service>" selector is dropped when it
// is the redundant alias of a sole-definer catalog service — i.e. its bare form
// also exists in Services and points at the SAME *Service. Two installed
// catalogs genuinely colliding on a name keep both qualified selectors (there is
// no bare form to alias). Used by `list`, the MCP server, and lint/doctor's
// all-services path so a service shows once.
func (l *Loaded) CanonicalNames() []string {
	names := make([]string, 0, len(l.Services))
	for sel, svc := range l.Services {
		if _, bareName, ok := strings.Cut(sel, ":"); ok {
			if bareSvc, present := l.Services[bareName]; present && bareSvc == svc {
				continue // redundant qualified alias of the sole-definer bare form
			}
		}
		names = append(names, sel)
	}
	sort.Strings(names)
	return names
}

// ConfigDir resolves the labctl config directory, honoring (in order):
// LABCTL_CONFIG_DIR, $XDG_CONFIG_HOME/labctl, then ~/.config/labctl.
func ConfigDir() string {
	if d := os.Getenv("LABCTL_CONFIG_DIR"); d != "" {
		return d
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "labctl")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "labctl")
	}
	return filepath.Join(home, ".config", "labctl")
}

// Load reads and merges the config dir. A missing dir or missing config.yaml is
// not an error (you get global defaults + whatever services exist). Returns an
// error only on malformed YAML or a failed service validation.
func Load(dir string) (*Loaded, error) {
	if dir == "" {
		dir = ConfigDir()
	}
	l := &Loaded{Dir: dir, Services: map[string]*Service{}, Origins: map[string]Origin{}, Ambiguous: map[string][]string{}}

	// Global config (optional). The config model is fully closed, so decode
	// strictly (KnownFields) — a typo'd top-level key is a config error (exit 2)
	// instead of being silently dropped. An empty file (io.EOF) is valid.
	cfgPath := filepath.Join(dir, "config.yaml")
	if b, err := os.ReadFile(cfgPath); err == nil {
		dec := yaml.NewDecoder(bytes.NewReader(b))
		dec.KnownFields(true)
		if err := dec.Decode(&l.Config); err != nil && !errors.Is(err, io.EOF) {
			return nil, &ConfigError{Err: fmt.Errorf("parse %s: %w", cfgPath, err)}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", cfgPath, err)
	}
	applyConfigDefaults(&l.Config)
	l.Config.Secrets = NormalizeSecrets(l.Config)
	if err := ValidateConfig(&l.Config); err != nil {
		return nil, fmt.Errorf("%s: %w", cfgPath, err)
	}

	// Per-user profile (optional). Loaded ONCE before the services loop and
	// applied per-service after mergeDefaults. The profile is the SOLE binding
	// mechanism: absent (or for an unbound service), a manifest stays portable —
	// structurally valid but incomplete (no base_url/refs) until a profile binds
	// it, which ValidateComplete enforces at execute time. A malformed profile or
	// unknown field is a config error (exit 2), like config.yaml.
	profile, err := LoadProfile(dir)
	if err != nil {
		return nil, err
	}
	l.Profile = profile

	// Embedded catalog (built-in portable manifests). These are the fallback
	// every config gets for free; a local services/<name>.yaml overrides one by
	// name below. A malformed embedded manifest is a build-time bug, so a decode
	// failure here is fatal (and caught by the catalog tests in CI).
	for _, name := range catalog.Names() {
		data, ok := catalog.Manifest(name)
		if !ok {
			continue // unreachable: Names and Manifest share one index
		}
		svc, err := decodeService(data, "catalog:"+name+".yaml", dir, os.Stderr)
		if err != nil {
			return nil, fmt.Errorf("embedded catalog: %w", err)
		}
		if svc.Name == "" {
			svc.Name = name
		}
		finalizeService(svc, l.Config, profile)
		l.Services[svc.Name] = svc
		l.Origins[svc.Name] = OriginEmbedded
	}

	// Installed (named) catalogs, loaded AFTER the embedded floor and BEFORE the
	// local services/ dir, so precedence is local > installed catalogs > embedded.
	// An installed catalog shadows the embedded one of the same name; two installed
	// catalogs claiming one name is a hard error (fail-closed). A missing catalogs/
	// dir is not an error.
	if err := loadInstalledCatalogs(l, profile); err != nil {
		return nil, err
	}

	// Local service manifests (optional dir). A local file overrides the embedded
	// (or installed-catalog) service of the same name — that is the feature, not a
	// duplicate error. Two LOCAL files claiming the same name is still a real
	// duplicate.
	svcDir := filepath.Join(dir, "services")
	entries, err := os.ReadDir(svcDir)
	if err != nil {
		if os.IsNotExist(err) {
			warnOrphanProfileBindings(profile, l.Services, os.Stderr)
			return l, nil
		}
		return nil, fmt.Errorf("read %s: %w", svcDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !isYAML(e.Name()) {
			continue
		}
		path := filepath.Join(svcDir, e.Name())
		svc, err := loadService(path, dir, os.Stderr) // dir = config root; spec: paths resolve relative to it
		if err != nil {
			return nil, err
		}
		if svc.Name == "" {
			svc.Name = strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".yaml"), ".yml")
		}
		finalizeService(svc, l.Config, profile)
		prior := l.Origins[svc.Name]
		_, wasAmbiguous := l.Ambiguous[svc.Name]
		switch {
		case prior == OriginLocal || prior == OriginOverride:
			return nil, fmt.Errorf("duplicate service name %q in %s", svc.Name, path)
		case prior == OriginEmbedded || prior.IsCatalog() || wasAmbiguous:
			// Local shadows the embedded one, a (sole-definer) installed catalog,
			// or an otherwise-ambiguous bare name claimed by multiple installed
			// catalogs — a local file always wins and resolves the ambiguity.
			l.Origins[svc.Name] = OriginOverride
		default:
			l.Origins[svc.Name] = OriginLocal
		}
		delete(l.Ambiguous, svc.Name) // a local file always resolves any prior ambiguity
		l.Services[svc.Name] = svc
	}
	// A binding for a service that never loaded is most likely a typo or a stale
	// entry — warn (non-fatal, mirroring loadService's spec-failure warning) so a
	// partial config dir still loads while the mismatch is surfaced.
	warnOrphanProfileBindings(profile, l.Services, os.Stderr)
	return l, nil
}

// loadInstalledCatalogs merges every manifest under <dir>/catalogs/<cat>/ into l,
// after the embedded floor and before the local services/ dir. Catalog subdirs
// are visited in sorted name order and each catalog's manifests in sorted file
// order, so the load is deterministic.
//
// Every installed-catalog service is ALWAYS addressable via its qualified
// "<catalog>:<service>" selector — registered unconditionally, regardless of any
// bare-name collision. The bare name additionally resolves when exactly one
// installed catalog defines it (shadowing the embedded floor, origin →
// catalog:<cat>); when MORE than one does, the bare name is left unresolved
// (recorded in l.Ambiguous, any embedded floor entry of that name removed) —
// looking it up is a hard error, never a silent pick. A duplicate service name
// within ONE catalog is still a hard error. A missing catalogs/ dir is not an
// error; a malformed installed manifest fails Load (consistent with services/).
func loadInstalledCatalogs(l *Loaded, profile *Profile) error {
	catalogsDir := CatalogsDir(l.Dir)
	entries, err := os.ReadDir(catalogsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", catalogsDir, err)
	}
	cats := make([]string, 0, len(entries))
	for _, e := range entries {
		// Skip non-dirs and dot-prefixed dirs (a crashed `catalog add` can leave a
		// .tmp-* staging dir behind; loading it as a phantom catalog would
		// otherwise pollute bare-name resolution with a stale definer).
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		cats = append(cats, e.Name())
	}
	sort.Strings(cats)

	// bareDefiners tracks, for every bare service name seen across installed
	// catalogs, the (sorted, by construction — cats is visited in sorted order)
	// list of catalogs that define it.
	bareDefiners := map[string][]string{}

	for _, cat := range cats {
		catDir := filepath.Join(catalogsDir, cat)
		files, err := os.ReadDir(catDir)
		if err != nil {
			return fmt.Errorf("read %s: %w", catDir, err)
		}
		names := make([]string, 0, len(files))
		for _, f := range files {
			if f.IsDir() || !isYAML(f.Name()) {
				continue // ignore .labctl-catalog.json and any non-YAML
			}
			names = append(names, f.Name())
		}
		sort.Strings(names)
		seenInCat := map[string]bool{}
		for _, fname := range names {
			path := filepath.Join(catDir, fname)
			b, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			svc, err := decodeService(b, fmt.Sprintf("catalogs/%s/%s", cat, fname), l.Dir, os.Stderr)
			if err != nil {
				return err
			}
			if svc.Name == "" {
				svc.Name = strings.TrimSuffix(strings.TrimSuffix(fname, ".yaml"), ".yml")
			}
			finalizeService(svc, l.Config, profile)

			// A duplicate service name WITHIN one catalog is still a real error —
			// only cross-catalog name reuse is allowed (disambiguated by selector).
			if seenInCat[svc.Name] {
				return fmt.Errorf("service %q defined more than once in catalog %q", svc.Name, cat)
			}
			seenInCat[svc.Name] = true

			selector := cat + ":" + svc.Name
			l.Services[selector] = svc
			l.Origins[selector] = catalogOrigin(cat)
			bareDefiners[svc.Name] = append(bareDefiners[svc.Name], cat)
		}
	}

	for name, definers := range bareDefiners {
		if len(definers) == 1 {
			cat := definers[0]
			l.Services[name] = l.Services[cat+":"+name] // shadows the embedded floor (or an unset bare name)
			l.Origins[name] = catalogOrigin(cat)
			continue
		}
		// More than one installed catalog defines this name: the bare selector
		// resolves to nothing (never silently pick one) — drop any embedded floor
		// entry and record the ambiguity for Lookup/CLI to report.
		delete(l.Services, name)
		delete(l.Origins, name)
		l.Ambiguous[name] = definers // already sorted: definers append in sorted cat order
	}
	return nil
}

// finalizeService applies global defaults then the user's profile binding (if
// any). Order matters: the profile wins over the manifest but inherits any global
// default the binding leaves unset. Shared by the embedded and local load paths.
func finalizeService(svc *Service, cfg Config, profile *Profile) {
	mergeDefaults(svc, cfg)
	if profile != nil {
		if b, ok := profile.Services[svc.Name]; ok {
			applyProfile(svc, b)
		}
	}
}

// warnOrphanProfileBindings emits a non-fatal warning for each profile binding
// whose service did not load (no matching services/<name>.yaml). It is lenient
// by design — a profile may legitimately carry entries for services not yet
// added — but a silent drop is inconsistent with the strict-decode philosophy
// elsewhere, so we surface it. Names are sorted for deterministic output.
func warnOrphanProfileBindings(profile *Profile, services map[string]*Service, warn io.Writer) {
	if profile == nil || warn == nil {
		return
	}
	var orphans []string
	for name := range profile.Services {
		if _, ok := services[name]; !ok {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans)
	for _, name := range orphans {
		_, _ = fmt.Fprintf(warn, "labctl: profile binds unknown service %q (no services/%s.yaml)\n", name, name)
	}
}

// LoadService reads a single manifest file (used by `labctl lint <file>`),
// applying global config defaults. Relative spec: paths resolve from the same
// directory as the manifest file (since there is no separate config root here).
func LoadService(path string, cfg Config) (*Service, error) {
	svc, err := loadService(path, filepath.Dir(path), os.Stderr)
	if err != nil {
		return nil, err
	}
	if svc.Name == "" {
		base := filepath.Base(path)
		svc.Name = strings.TrimSuffix(strings.TrimSuffix(base, ".yaml"), ".yml")
	}
	mergeDefaults(svc, cfg)
	return svc, nil
}

// mergeSpecCommands derives commands from svc.Spec and merges them under svc.Commands.
// Explicit commands: entries take precedence over inferred ones (same key → explicit wins).
func mergeSpecCommands(svc *Service, configDir string) error {
	inferred, err := InferredCommands(svc, configDir)
	if err != nil {
		return err
	}
	if len(inferred) == 0 {
		return nil
	}
	// Ensure the commands map exists before merging.
	if svc.Commands == nil {
		svc.Commands = make(map[string]Command, len(inferred))
	}
	for key, cmd := range inferred {
		if _, explicit := svc.Commands[key]; !explicit {
			svc.Commands[key] = cmd
		}
	}
	return nil
}

// loadService parses and validates a single manifest file. configDir is the
// root config directory used to resolve relative spec: file paths. When called
// from Load, configDir == l.Dir; when called from LoadService (lint), it
// defaults to the directory containing the manifest file.
func loadService(path, configDir string, warn io.Writer) (*Service, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return decodeService(b, path, configDir, warn)
}

// decodeService parses, structurally validates, and spec-infers a manifest from
// raw bytes. label names the source in errors/warnings (a file path for a local
// manifest, "catalog:<name>" for an embedded one). configDir resolves relative
// spec: paths; pass "" when there is no config root (a bare embedded manifest).
func decodeService(b []byte, label, configDir string, warn io.Writer) (*Service, error) {
	var svc Service
	if err := yaml.Unmarshal(b, &svc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", label, err)
	}
	if err := Validate(&svc); err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	// Inject spec-derived commands (Phase 2). Explicit commands: entries win.
	// A spec fetch/parse failure degrades ONLY this service: warn and keep its
	// statically-declared commands, rather than aborting the whole load and
	// taking down unrelated services.
	if err := mergeSpecCommands(&svc, configDir); err != nil {
		l := svc.Name
		if l == "" {
			l = label
		}
		_, _ = fmt.Fprintf(warn, "labctl: service %q: spec inference failed: %v (using static commands only)\n", l, err)
	}
	return &svc, nil
}

func applyConfigDefaults(c *Config) {
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Defaults.Timeout == "" {
		c.Defaults.Timeout = "60s"
	}
	if c.Defaults.Output == "" {
		c.Defaults.Output = "json"
	}
	if c.Defaults.MaxResponseBytes == 0 {
		c.Defaults.MaxResponseBytes = 64 << 20 // 64 MiB
	}
	if len(c.Secret.Command) == 0 {
		c.Secret.Command = append([]string(nil), DefaultResolverCommand...)
	}
	if c.Secret.Resolver == "" {
		c.Secret.Resolver = "op"
	}
}

// schemeAliases maps a provider's map key to a default URI scheme when the
// ProviderConfig leaves Scheme empty.
var schemeAliases = map[string]string{
	"onepassword": "op",
	"op":          "op",
}

// NormalizeSecrets returns the effective scheme-dispatched secrets config. When
// the new secrets.providers block is present it is returned with per-provider
// defaults applied; otherwise the legacy `secret:` block is folded into a single
// equivalent op provider, so existing configs keep working unchanged. Pure and
// idempotent (re-normalizing its own output is a no-op).
func NormalizeSecrets(cfg Config) SecretsConfig {
	out := SecretsConfig{}
	if cfg.Secrets.EnvOverride != nil {
		out.EnvOverride = cfg.Secrets.EnvOverride
	} else {
		v := cfg.Secret.EnvOverride
		out.EnvOverride = &v
	}

	if len(cfg.Secrets.Providers) > 0 {
		out.Providers = make(map[string]ProviderConfig, len(cfg.Secrets.Providers))
		for name, p := range cfg.Secrets.Providers {
			if p.Scheme == "" {
				if s, ok := schemeAliases[name]; ok {
					p.Scheme = s
				} else {
					p.Scheme = name
				}
			}
			if len(p.Command) == 0 && p.Scheme == "op" {
				p.Command = append([]string(nil), DefaultResolverCommand...)
			}
			out.Providers[name] = p
		}
		return out
	}

	// Legacy: synthesize a single op provider from the `secret:` block.
	cmd := cfg.Secret.Command
	if len(cmd) == 0 {
		cmd = append([]string(nil), DefaultResolverCommand...)
	}
	out.Providers = map[string]ProviderConfig{
		"onepassword": {
			Scheme:  "op",
			Command: append([]string(nil), cmd...),
		},
	}
	return out
}

func mergeDefaults(svc *Service, cfg Config) {
	if svc.Transport == "" {
		svc.Transport = "http"
	}
	if svc.Timeout == "" {
		svc.Timeout = cfg.Defaults.Timeout
	}
}

// TimeoutDuration parses the resolved timeout, falling back to 60s.
func (s *Service) TimeoutDuration() time.Duration {
	if d, err := time.ParseDuration(s.Timeout); err == nil {
		return d
	}
	return 60 * time.Second
}

// SortedServiceNames returns service selectors in stable order (for --list).
func (l *Loaded) SortedServiceNames() []string {
	names := make([]string, 0, len(l.Services))
	for n := range l.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}
