// Package catalog holds the portable service manifests compiled into the labctl
// binary via go:embed. They are the built-in catalog: a user's local
// <config-dir>/services/<name>.yaml overrides the embedded manifest of the same
// name, but absent any local manifest every service here is still available — so
// consumers no longer need to vendor copies of these files.
//
// This package is intentionally dependency-free (no manifest import) so it can be
// imported by the manifest loader without an import cycle. It serves raw YAML
// bytes; parsing/validation lives in the manifest package.
package catalog

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

// files embeds every portable manifest. The glob fails to compile if it matches
// nothing, so the catalog can never silently ship empty.
//
//go:embed services/*.yaml
var files embed.FS

// index maps a service name (the filename stem) to its embedded YAML bytes. Built
// once at package init from the embedded FS.
var index = buildIndex()

func buildIndex() map[string][]byte {
	m := map[string][]byte{}
	// The embed is a build-time invariant: a ReadDir/ReadFile failure here means a
	// corrupt or misbuilt binary, so fail loud at startup rather than silently
	// shipping a binary with no (or fewer) services that only breaks at runtime.
	entries, err := files.ReadDir("services")
	if err != nil {
		panic(fmt.Sprintf("embedded catalog corrupt: ReadDir(services): %v", err))
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		data, err := files.ReadFile("services/" + name)
		if err != nil {
			panic(fmt.Sprintf("embedded catalog corrupt: ReadFile(services/%s): %v", name, err))
		}
		m[strings.TrimSuffix(name, ".yaml")] = data
	}
	return m
}

// Names returns the embedded service names (filename stems) in sorted order.
func Names() []string {
	out := make([]string, 0, len(index))
	for n := range index {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Manifest returns the raw embedded YAML for a service name, and whether it
// exists in the catalog.
func Manifest(name string) ([]byte, bool) {
	b, ok := index[name]
	return b, ok
}
