package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// CatalogMetaFile is the per-catalog metadata file labctl writes into each
// installed catalog dir. It records where the catalog came from (a dir or a git
// URL, pinned to a commit) so `catalog update` can re-fetch and `catalog
// installed` can report provenance. It is NOT a manifest and the loader ignores it.
const CatalogMetaFile = ".labctl-catalog.json"

// CatalogMeta is the provenance record for one installed catalog.
type CatalogMeta struct {
	Name      string    `json:"name"`
	Source    string    `json:"source"`
	Type      string    `json:"type"`             // "git" | "dir"
	Ref       string    `json:"ref,omitempty"`    // git: requested ref (empty = default branch)
	Commit    string    `json:"commit,omitempty"` // git: resolved commit SHA
	AddedAt   time.Time `json:"added_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// namePattern restricts a service OR catalog name to a single, safe path
// segment: it must start with an alphanumeric and contain only lowercase
// letters, digits, '-' and '_'. Both a service name (used to build
// <config-dir>/services/<name>.yaml and catalog/<name>.yaml) and a catalog name
// (used to build <config-dir>/catalogs/<name>/) must satisfy it so neither can
// contain a separator or climb out of its directory.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// CatalogsDir returns the installed-catalogs root under a config dir.
func CatalogsDir(configDir string) string { return filepath.Join(configDir, "catalogs") }

// ValidateName is the single shared service/catalog-name guard against path
// traversal: name must be a single lowercase path segment
// (^[a-z0-9][a-z0-9_-]*$). It rejects "", ".", "..", "a/b", absolute paths, a
// leading '-', and uppercase. It returns a plain error; each caller wraps it in
// its own typed error (manifest *ConfigError / cli usage error → exit 2).
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("invalid name %q: must be a single path segment (^[a-z0-9][a-z0-9_-]*$)", name)
	}
	return nil
}

// readMetaFile reads and parses the metadata file from a catalog dir. found is
// false (with a nil error) when the file is simply absent.
func readMetaFile(catalogDir string) (meta CatalogMeta, found bool, err error) {
	b, err := os.ReadFile(filepath.Join(catalogDir, CatalogMetaFile))
	if err != nil {
		if os.IsNotExist(err) {
			return CatalogMeta{}, false, nil
		}
		return CatalogMeta{}, false, err
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return CatalogMeta{}, false, fmt.Errorf("parse %s: %w", filepath.Join(catalogDir, CatalogMetaFile), err)
	}
	return meta, true, nil
}

// InstalledCatalogs lists installed catalogs (sorted by name). A subdir whose
// metadata file is missing or unparseable yields a minimal CatalogMeta{Name:dir}
// — so `catalog remove` still works — rather than failing the whole listing.
func InstalledCatalogs(configDir string) ([]CatalogMeta, error) {
	catalogsDir := CatalogsDir(configDir)
	entries, err := os.ReadDir(catalogsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", catalogsDir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		// Skip non-dirs and staging/hidden dirs (.tmp-*) so a crashed install
		// mid-rename never shows up as a catalog.
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	out := make([]CatalogMeta, 0, len(names))
	for _, name := range names {
		meta, found, err := readMetaFile(filepath.Join(catalogsDir, name))
		if err != nil || !found {
			meta = CatalogMeta{Name: name} // synthesize minimal; don't fail the listing
		}
		if meta.Name == "" {
			meta.Name = name
		}
		out = append(out, meta)
	}
	return out, nil
}

// ReadCatalogMeta reads one installed catalog's metadata. found is false when the
// catalog dir does not exist. A dir with no metadata file yields a synthesized
// minimal CatalogMeta{Name:name} (found=true) — enough to remove it, though not
// to update it (no recorded source). A corrupt metadata file is an error.
func ReadCatalogMeta(configDir, name string) (CatalogMeta, bool, error) {
	if err := ValidateName(name); err != nil {
		return CatalogMeta{}, false, &ConfigError{Err: err}
	}
	dir := filepath.Join(CatalogsDir(configDir), name)
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return CatalogMeta{}, false, nil
		}
		return CatalogMeta{}, false, err
	}
	if !info.IsDir() {
		return CatalogMeta{}, false, nil
	}
	meta, found, err := readMetaFile(dir)
	if err != nil {
		return CatalogMeta{}, false, err
	}
	if !found {
		return CatalogMeta{Name: name}, true, nil
	}
	if meta.Name == "" {
		meta.Name = name
	}
	return meta, true, nil
}

// InstallCatalog atomically installs a catalog of portable manifests under
// <configDir>/catalogs/<meta.Name>/, writing the provenance metadata file too.
// files is keyed by manifest filename → bytes; each key is sanitized and must be
// a single .yaml/.yml path segment (a second-layer path-traversal guard even
// though the caller pre-filters). Everything is staged in a sibling temp dir and
// swapped into place, so a failure never leaves a half-installed catalog. A final
// dir that already exists is an error unless force is set (then it is replaced).
func InstallCatalog(configDir string, meta CatalogMeta, files map[string][]byte, force bool) error {
	if err := ValidateName(meta.Name); err != nil {
		return &ConfigError{Err: err}
	}
	catalogsDir := CatalogsDir(configDir)
	if err := os.MkdirAll(catalogsDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", catalogsDir, err)
	}
	final := filepath.Join(catalogsDir, meta.Name)

	tmp, err := os.MkdirTemp(catalogsDir, ".tmp-"+meta.Name+"-")
	if err != nil {
		return fmt.Errorf("creating staging dir: %w", err)
	}
	// Never leave a half-installed catalog: clean up the staging dir on any path.
	// After a successful rename tmp no longer exists and RemoveAll is a no-op.
	defer func() { _ = os.RemoveAll(tmp) }()

	for key, data := range files {
		base := filepath.Base(key)
		if base != key || base == "." || base == ".." {
			return &ConfigError{Err: fmt.Errorf("invalid manifest filename %q: must be a bare file name", key)}
		}
		if !isYAML(base) {
			return &ConfigError{Err: fmt.Errorf("invalid manifest filename %q: must end in .yaml or .yml", key)}
		}
		dest := filepath.Join(tmp, base)
		// Path-traversal guard: the cleaned join must stay under the staging dir.
		if !strings.HasPrefix(dest, tmp+string(filepath.Separator)) {
			return &ConfigError{Err: fmt.Errorf("invalid manifest filename %q: escapes the catalog dir", key)}
		}
		if err := os.WriteFile(dest, data, 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", dest, err)
		}
	}

	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal catalog meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, CatalogMetaFile), metaBytes, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", CatalogMetaFile, err)
	}

	switch _, statErr := os.Stat(final); {
	case statErr == nil:
		if !force {
			return &ConfigError{Err: fmt.Errorf("catalog %q already exists; pass --force or use 'catalog update'", meta.Name)}
		}
		if err := os.RemoveAll(final); err != nil {
			return fmt.Errorf("removing existing %s: %w", final, err)
		}
	case !os.IsNotExist(statErr):
		return fmt.Errorf("stat %s: %w", final, statErr)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("installing catalog %q: %w", meta.Name, err)
	}
	return nil
}

// RemoveCatalog deletes an installed catalog's dir. A name that is not installed
// is a *ConfigError (exit 2), not a silent success.
func RemoveCatalog(configDir, name string) error {
	if err := ValidateName(name); err != nil {
		return &ConfigError{Err: err}
	}
	dir := filepath.Join(CatalogsDir(configDir), name)
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return &ConfigError{Err: fmt.Errorf("catalog %q is not installed", name)}
		}
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return &ConfigError{Err: fmt.Errorf("catalog %q is not installed", name)}
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("removing catalog %q: %w", name, err)
	}
	return nil
}
