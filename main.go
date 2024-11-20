package main

import (
	"github.com/bnema/gordon/cmd"
	"github.com/charmbracelet/log"
)

var (
	version string
	commit  string
	date    string
)

func main() {
	log.Info("Starting Gordon", "version", version, "commit", commit, "date", date)
	cmd.ExecuteCLI(version, commit, date)
}
