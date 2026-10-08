// Package childenv builds the environment for secret-tool child processes
// (pass, gpg, sops). execve makes a child dumpable again, so a same-user
// process can read its /proc/<pid>/environ: Gordon's own GORDON_* values
// (token secrets, API tokens) must never be inherited.
package childenv

import (
	"os"
	"strings"
)

// Env returns the current environment without GORDON_* variables.
func Env() []string {
	return filter(os.Environ())
}

func filter(environ []string) []string {
	kept := make([]string, 0, len(environ))
	for _, entry := range environ {
		if !strings.HasPrefix(entry, "GORDON_") {
			kept = append(kept, entry)
		}
	}
	return kept
}
