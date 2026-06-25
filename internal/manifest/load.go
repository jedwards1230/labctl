package manifest

import (
	"fmt"
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

	// Global config (optional).
	cfgPath := filepath.Join(dir, "config.yaml")
	if b, err := os.ReadFile(cfgPath); err == nil {
		if err := yaml.Unmarshal(b, &l.Config); err != nil {
			return nil, fmt.Errorf("parse %s: %w", cfgPath, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", cfgPath, err)
	}
	applyConfigDefaults(&l.Config)

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
		svc, err := loadService(path)
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
// applying global config defaults.
func LoadService(path string, cfg Config) (*Service, error) {
	svc, err := loadService(path)
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

func loadService(path string) (*Service, error) {
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
	if len(c.Secret.Command) == 0 {
		c.Secret.Command = append([]string(nil), DefaultResolverCommand...)
	}
	if c.Secret.Resolver == "" {
		c.Secret.Resolver = "op"
	}
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
