package manifest

import (
	"fmt"
	"io"

	"github.com/jedwards1230/labctl/catalog"
)

// The embedded catalog is the built-in set of portable manifests compiled into
// the binary (the top-level catalog package). The loader merges it with the local services/
// dir: a local manifest of the same name overrides the embedded one, but every
// service present only in the catalog is still available. These helpers expose
// the catalog directly, for `labctl catalog list/show` and tests.

// CatalogNames returns the embedded catalog's service names, sorted.
func CatalogNames() []string { return catalog.Names() }

// CatalogManifest returns the raw embedded YAML for a service name (used by
// `labctl catalog show` to dump a manifest for forking into a local override).
func CatalogManifest(name string) ([]byte, bool) { return catalog.Manifest(name) }

// CatalogService decodes and structurally validates one embedded manifest. It
// applies no global defaults and no profile binding — it is the manifest exactly
// as shipped. Relative spec: paths (none today) have no config root, so spec
// inference is skipped.
func CatalogService(name string) (*Service, error) {
	data, ok := catalog.Manifest(name)
	if !ok {
		return nil, fmt.Errorf("no embedded service %q", name)
	}
	svc, err := decodeService(data, "catalog:"+name, "", io.Discard)
	if err != nil {
		return nil, err
	}
	if svc.Name == "" {
		svc.Name = name
	}
	return svc, nil
}

// CatalogServices decodes every embedded manifest, in sorted name order. An error
// in any one (a malformed catalog entry) fails the whole call.
func CatalogServices() ([]*Service, error) {
	names := catalog.Names()
	out := make([]*Service, 0, len(names))
	for _, n := range names {
		svc, err := CatalogService(n)
		if err != nil {
			return nil, err
		}
		out = append(out, svc)
	}
	return out, nil
}
