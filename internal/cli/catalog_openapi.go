package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/transport"
)

// `catalog add --openapi <source>` materializes a portable manifest from an
// OpenAPI 3.x document (URL or local file) and installs it as a single-service
// named catalog — see catalog_install.go for the dir/git add path this sits
// alongside. The spec is parsed once at add-time (internal/manifest's
// GenerateManifestFromSpec); labctl does NOT vendor the spec file and does NOT
// emit a `spec:` reference, so the installed manifest is self-contained like
// any other portable manifest.

const (
	// openapiFetchTimeout bounds the whole HTTP fetch (connect + read).
	openapiFetchTimeout = 30 * time.Second
	// openapiMaxBodyBytes caps the response body so a hostile/huge document
	// can't exhaust memory. 16 MiB is generous for an OpenAPI document (even a
	// large one is typically well under 1 MiB).
	openapiMaxBodyBytes = 16 << 20
	// openapiMaxRedirects bounds the redirect chain a fetch will follow.
	openapiMaxRedirects = 5
)

// catalogAddOpenAPI fetches/reads source as an OpenAPI 3.x document, generates
// a portable manifest for it (inferring the service name from info.title when
// name is empty), validates it through the same gate `catalog add <dir>` uses,
// and installs it as a single-service catalog.
func (r *runner) catalogAddOpenAPI(source, name string, force bool) error {
	specBytes, err := fetchOpenAPISource(source)
	if err != nil {
		return err
	}

	if name == "" {
		inferred, err := manifest.InferServiceName(specBytes)
		if err != nil {
			return err
		}
		if inferred == "" {
			return &usageError{"OpenAPI document has no info.title to infer a service name from; pass --name"}
		}
		if err := manifest.ValidateCatalogName(inferred); err != nil {
			return &usageError{fmt.Sprintf("inferred name %q from the OpenAPI document's info.title is not a valid service/catalog name (^[a-z0-9][a-z0-9_-]*$); pass --name", inferred)}
		}
		name = inferred
	} else if err := manifest.ValidateCatalogName(name); err != nil {
		return err
	}

	data, err := manifest.GenerateManifestFromSpec(name, specBytes)
	if err != nil {
		return err
	}
	// GenerateManifestFromSpec already validates its own output; call the
	// install-time gate explicitly too, since it's the same fail-closed check
	// every other catalog source goes through before anything is written.
	if _, err := manifest.ValidatePortableManifest(data); err != nil {
		return err
	}

	now := time.Now().UTC()
	meta := manifest.CatalogMeta{Name: name, Source: source, Type: "openapi", AddedAt: now, UpdatedAt: now}
	files := map[string][]byte{name + ".yaml": data}
	if err := manifest.InstallCatalog(r.configDir(), meta, files, force); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(r.stderr, "installed catalog %q (1 manifest) from OpenAPI document %s\n", name, source)
	return nil
}

// fetchOpenAPISource reads an OpenAPI document from a local file path or an
// http(s) URL. This is a DEDICATED fetch path for `catalog add --openapi`,
// separate from internal/manifest's fetchSpec/fetchURL (the cached, 24h-TTL
// `spec:` load-time path) — that path is reused on every invocation of a
// service with `spec:` set, so it caches; this one runs once at add-time, so
// it doesn't need to, but it does add an explicit response-size cap and a
// bounded redirect chain on top of the same timeout, since the document is
// untrusted remote input either way.
//
// Security posture: this intentionally does NOT add private-IP/SSRF blocking.
// OpenAPI documents commonly live on the LAN in this homelab (the same reason
// `catalog add <git-url>` and the `spec:` URL fetch don't block RFC1918
// either) — the trust model is "a user fetching a URL they chose," not
// "untrusted third party supplies the URL." The hardening here is a scheme
// allow-list, a request timeout, a response-size cap, and a bounded redirect
// chain — not destination filtering.
func fetchOpenAPISource(source string) ([]byte, error) {
	switch {
	case strings.HasPrefix(source, "http://"), strings.HasPrefix(source, "https://"):
		return fetchOpenAPIURL(source)
	case strings.Contains(source, "://"):
		return nil, &usageError{fmt.Sprintf("--openapi source %q must be an http(s):// URL or a local file path", source)}
	default:
		b, err := os.ReadFile(source)
		if err != nil {
			return nil, &usageError{fmt.Sprintf("read OpenAPI document %q: %v", source, err)}
		}
		return b, nil
	}
}

// fetchOpenAPIURL downloads an OpenAPI document over HTTP(S) with a bounded
// timeout, a capped redirect chain (each hop re-checked for scheme), and a
// hard limit on the response body size.
func fetchOpenAPIURL(rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), openapiFetchTimeout)
	defer cancel()

	client := &http.Client{
		Timeout: openapiFetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= openapiMaxRedirects {
				return fmt.Errorf("stopped after %d redirects", openapiMaxRedirects)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("refusing redirect to non-http(s) scheme %q", req.URL.Scheme)
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, &usageError{fmt.Sprintf("invalid OpenAPI URL %q: %v", rawURL, err)}
	}
	resp, err := client.Do(req)
	if err != nil {
		// A transport failure (connection refused, DNS, timeout, …) is a
		// runtime network problem, not a usage/decode one → exit 5.
		return nil, &transport.NetworkError{Err: fmt.Errorf("fetch OpenAPI document %s: %w", rawURL, err)}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// The endpoint answered but did not return the document → decode (exit 6).
		return nil, &manifest.DecodeError{Err: fmt.Errorf("fetch OpenAPI document %s: HTTP %d", rawURL, resp.StatusCode)}
	}

	limited := http.MaxBytesReader(nil, resp.Body, openapiMaxBodyBytes)
	b, err := io.ReadAll(limited)
	if err != nil {
		// Exceeding the size cap (or any other body-read failure once the status
		// is OK) is treated as a malformed/oversized document → decode (exit 6).
		return nil, &manifest.DecodeError{Err: fmt.Errorf("read OpenAPI document %s (exceeds %d byte limit or connection failed): %w", rawURL, openapiMaxBodyBytes, err)}
	}
	return b, nil
}
