package main

import (
	"os"

	"gordon/internal/adapters/in/cli"
)

// Build information set by goreleaser via ldflags
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	// Set version information for the CLI
	cli.SetVersionInfo(version, commit, date)

	if err := cli.NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
