package main

import (
	"github.com/bnema/gordon/cmd"
)

var (
	version string
	commit  string
	date    string
)

func main() {
	cmd.ExecuteCLI(version, commit, date)
}
