// Command labctl is a manifest-driven CLI for homelab service APIs. A service is
// a YAML manifest; the binary knows nothing service-specific. See the README.
package main

import (
	"os"
	"runtime/debug"

	"github.com/jedwards1230/labctl/internal/cli"
)

// version is the ldflags sink: release builds stamp it via
// -ldflags "-X main.version=vX.Y.Z"; otherwise it stays the "dev" default.
var version = "dev"

// buildInfoReader is the seam over runtime/debug.ReadBuildInfo so resolveVersion
// is unit-testable without a real module build.
type buildInfoReader func() (*debug.BuildInfo, bool)

// resolveVersion returns the ldflags value when a release build stamped it (i.e.
// it is not the "dev" default). Otherwise it falls back to the module version
// recorded in build info — which the toolchain sets for
// `go install github.com/jedwards1230/labctl@vX.Y.Z` — when that is a real tag
// (not empty, not the "(devel)" placeholder). When neither yields a real
// version it stays "dev".
func resolveVersion(ldflags string, read buildInfoReader) string {
	if ldflags != "dev" {
		return ldflags
	}
	if info, ok := read(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}

func main() {
	cli.Version = resolveVersion(version, debug.ReadBuildInfo)
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
