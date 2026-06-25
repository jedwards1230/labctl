// Command labctl is a manifest-driven CLI for homelab service APIs. A service is
// a YAML manifest; the binary knows nothing service-specific. See the README.
package main

import (
	"os"

	"github.com/jedwards1230/labctl/internal/cli"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cli.Version = version
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
