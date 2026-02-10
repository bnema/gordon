package main

import (
	"os"

	"github.com/bnema/gordon/internal/adapters/in/cli"
	versionpkg "github.com/bnema/gordon/pkg/version"
)

// Build information set by goreleaser via ldflags
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	// Set version information globally and for the CLI
	versionpkg.Set(version, commit, date)
	cli.SetVersionInfo(version, commit, date)

	if err := cli.NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
