// Package childenv builds the environment for secret-tool child processes
// (pass, gpg, sops). execve makes a child dumpable again, so a same-user
// process can read its /proc/<pid>/environ: Gordon's own GORDON_* values
// (token secrets, API tokens) must never be inherited.
package childenv

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// CommandContext is exec.CommandContext with Env set to Env(). Use it for
// every child process the daemon starts.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- callers pass constant binaries with validated args
	cmd.Env = Env()
	return cmd
}

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
