// Package helpers provides Docker-related test utilities.
package helpers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/docker/docker/client"
)

const (
	TestImageName = "gordon:v3-test"
	TestAppImage  = "ghcr.io/bnema/go-hello-world-http:latest"
)

// BuildGordonImage builds the Gordon Docker image for testing.
func BuildGordonImage(t *testing.T, ctx context.Context) error {
	// Check if image already exists
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("failed to create docker client: %w", err)
	}
	defer cli.Close()

	_, _, err = cli.ImageInspectWithRaw(ctx, TestImageName)
	if err == nil {
		t.Logf("Using existing Gordon image: %s", TestImageName)
		return nil
	}

	// Build the Go binary
	buildCmd := exec.Command("go", "build", "-o", "/tmp/gordon-test", "./main.go")
	buildCmd.Dir = "../.."
	if output, err := buildCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to build binary: %w\n%s", err, output)
	}

	// Build Docker image
	dockerCmd := exec.Command("docker", "build", "-t", TestImageName, ".")
	dockerCmd.Dir = "../.."
	if output, err := dockerCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to build image: %w\n%s", err, output)
	}

	t.Logf("Built Gordon image: %s", TestImageName)
	return nil
}

// DetectDockerHost returns the Docker host for rootless Docker.
func DetectDockerHost() string {
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		return host
	}
	uid := os.Getuid()
	return fmt.Sprintf("unix:///run/user/%d/docker.sock", uid)
}

// GetDockerSocketPath returns the host path to the Docker socket for mounting.
// It strips the "unix://" prefix from the DOCKER_HOST URL.
func GetDockerSocketPath() string {
	host := DetectDockerHost()
	// Remove unix:// prefix to get the host path
	if len(host) > 7 && host[:7] == "unix://" {
		return host[7:]
	}
	return host
}
