package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/client"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/pkg/registrypush"
)

// pushImageOps abstracts container image operations needed by push commands.
// The real implementation uses Docker SDK + registrypush; tests inject fakes.
type pushImageOps interface {
	// Tag creates a new tag for an existing local image.
	Tag(ctx context.Context, sourceRef, targetRef string) error
	// Push uploads a local image to its registry using chunked blob uploads.
	Push(ctx context.Context, ref string) error
	// Exists checks whether a local image exists.
	Exists(ctx context.Context, imageRef string) (bool, error)
	// Build runs a container image build. Only Docker with buildx is supported;
	// other runtimes return an actionable error.
	Build(ctx context.Context, args []string) error
}

// dockerImageOps implements pushImageOps using the Docker SDK and registrypush.
type dockerImageOps struct {
	cli         client.APIClient
	insecureTLS bool
	progress    io.Writer
}

// newDockerImageOps creates a dockerImageOps from the Docker environment.
func newDockerImageOps(insecureTLS bool, progress io.Writer) (*dockerImageOps, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}
	return &dockerImageOps{
		cli:         cli,
		insecureTLS: insecureTLS,
		progress:    progress,
	}, nil
}

// newImageOpsFromFlags resolves TLS settings from CLI flags/config and creates ops.
func newImageOpsFromFlags() (pushImageOps, error) {
	_, _, insecureTLS, _ := remote.ResolveRemote(remoteFlag, tokenFlag, insecureTLSFlag)
	return newDockerImageOps(insecureTLS, os.Stderr)
}

func (d *dockerImageOps) Tag(ctx context.Context, sourceRef, targetRef string) error {
	if err := d.cli.ImageTag(ctx, sourceRef, targetRef); err != nil {
		return fmt.Errorf("failed to tag %s as %s: %w", sourceRef, targetRef, err)
	}
	return nil
}

func (d *dockerImageOps) Push(ctx context.Context, ref string) error {
	pusher := registrypush.New(
		registrypush.WithProgress(d.progress),
		registrypush.WithInsecureTLS(d.insecureTLS),
	)
	return pusher.Push(ctx, ref)
}

func (d *dockerImageOps) Exists(ctx context.Context, imageRef string) (bool, error) {
	_, err := d.cli.ImageInspect(ctx, imageRef)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to inspect image %s: %w", imageRef, err)
	}
	return true, nil
}

func (d *dockerImageOps) Build(ctx context.Context, args []string) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf(
			"image build requires Docker with buildx\n\n" +
				"Podman and other runtimes are not supported for --build.\n" +
				"Build the image manually and push with:\n" +
				"  podman build -t <image> .\n" +
				"  gordon push <image>",
		)
	}
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker buildx build failed: %w", err)
	}
	return nil
}
