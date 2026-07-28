package releasesmoke

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CommandRunner executes container engine commands.
type CommandRunner interface {
	Run(ctx context.Context, args ...string) (stdout string, err error)
}

type execRunner struct {
	name string
}

func (r execRunner) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.name, args...) // #nosec G204 -- release gate invokes docker/podman explicitly.
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s %s: %w", r.name, strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

// DockerRunner returns a docker CLI runner.
func DockerRunner() CommandRunner { return execRunner{name: "docker"} }

// PodmanRunner returns a podman CLI runner.
func PodmanRunner() CommandRunner { return execRunner{name: "podman"} }

func runOutput(ctx context.Context, runner CommandRunner, args ...string) (string, error) {
	return runner.Run(ctx, args...)
}

func runQuiet(ctx context.Context, runner CommandRunner, args ...string) error {
	_, err := runner.Run(ctx, args...)
	return err
}
