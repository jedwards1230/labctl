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

	"gopkg.in/yaml.v3"
)

// DefaultResolverCommand is the secret resolver used when config.yaml omits one.
var DefaultResolverCommand = []string{"op", "read", "{ref}"}

// Loaded is the merged result of a config dir: the global config plus every
// service manifest, keyed by selector name.
type Loaded struct {
	Config   Config
	Services map[string]*Service
	Dir      string
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
	l := &Loaded{Dir: dir, Services: map[string]*Service{}}

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

	// Service manifests (optional dir).
	svcDir := filepath.Join(dir, "services")
	entries, err := os.ReadDir(svcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return nil, fmt.Errorf("read %s: %w", svcDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !isYAML(e.Name()) {
			continue
		}
		path := filepath.Join(svcDir, e.Name())
		svc, err := loadService(path, dir) // dir = config root; spec: paths resolve relative to it
		if err != nil {
			return nil, err
		}
		if svc.Name == "" {
			svc.Name = strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".yaml"), ".yml")
		}
		mergeDefaults(svc, l.Config)
		if other, dup := l.Services[svc.Name]; dup {
			return nil, fmt.Errorf("duplicate service name %q in %s (also defined elsewhere as %s)", svc.Name, path, other.Name)
		}
		l.Services[svc.Name] = svc
	}
	return l, nil
}

// LoadService reads a single manifest file (used by `labctl lint <file>`),
// applying global config defaults. Relative spec: paths resolve from the same
// directory as the manifest file (since there is no separate config root here).
func LoadService(path string, cfg Config) (*Service, error) {
	svc, err := loadService(path, filepath.Dir(path))
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
func loadService(path, configDir string) (*Service, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var svc Service
	if err := yaml.Unmarshal(b, &svc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := Validate(&svc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// Inject spec-derived commands (Phase 2). Explicit commands: entries win.
	if err := mergeSpecCommands(&svc, configDir); err != nil {
		return nil, fmt.Errorf("%s: spec: %w", path, err)
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
