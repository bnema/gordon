package main

import (
	"os"

	"gordon/cmd"
)

// Build information set by goreleaser via ldflags
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	// Set version information for the CLI
	cmd.SetVersionInfo(version, commit, date)
	
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}