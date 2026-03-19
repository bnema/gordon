package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

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
	cli          client.APIClient
	insecureTLS  bool
	progress     io.Writer
	remoteClient *remote.Client // nil when local
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
	remoteURL, token, insecureTLS, _ := remote.ResolveRemote(remoteFlag, tokenFlag, insecureTLSFlag)
	ops, err := newDockerImageOps(insecureTLS, os.Stderr)
	if err != nil {
		return nil, err
	}
	if remoteURL != "" && token != "" {
		var clientOpts []remote.ClientOption
		clientOpts = append(clientOpts, remote.WithToken(token))
		if insecureTLS {
			clientOpts = append(clientOpts, remote.WithInsecureTLS(true))
		}
		ops.remoteClient = remote.NewClient(remoteURL, clientOpts...)
	}
	return ops, nil
}

func (d *dockerImageOps) Tag(ctx context.Context, sourceRef, targetRef string) error {
	if err := d.cli.ImageTag(ctx, sourceRef, targetRef); err != nil {
		return fmt.Errorf("failed to tag %s as %s: %w", sourceRef, targetRef, err)
	}
	return nil
}

func (d *dockerImageOps) Push(ctx context.Context, ref string) error {
	opts := []registrypush.Option{
		registrypush.WithProgress(d.progress),
		registrypush.WithInsecureTLS(d.insecureTLS),
	}

	if d.remoteClient != nil {
		authHeader, err := d.exchangeRegistryAuth(ctx)
		if err != nil {
			return fmt.Errorf("failed to obtain registry credentials: %w", err)
		}
		opts = append(opts, registrypush.WithAuth(authHeader))

		pusher := registrypush.New(opts...)
		err = pusher.Push(ctx, ref)
		if err != nil && isAuthError(err) {
			// Token may have expired mid-push; refresh once and retry.
			authHeader, refreshErr := d.exchangeRegistryAuth(ctx)
			if refreshErr != nil {
				return fmt.Errorf("failed to refresh registry credentials after auth error: %w (original: %v)", refreshErr, err)
			}
			retryOpts := []registrypush.Option{
				registrypush.WithProgress(d.progress),
				registrypush.WithInsecureTLS(d.insecureTLS),
				registrypush.WithAuth(authHeader),
			}
			pusher = registrypush.New(retryOpts...)
			return pusher.Push(ctx, ref)
		}
		return err
	}

	pusher := registrypush.New(opts...)
	return pusher.Push(ctx, ref)
}

// isAuthError returns true if the error indicates an HTTP 401 authentication failure.
// Prefers typed HTTPError from the remote client; falls back to string matching for
// errors from registrypush which uses its own error format.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *remote.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusUnauthorized
	}
	msg := err.Error()
	return strings.Contains(msg, "status 401") || strings.Contains(msg, "401 Unauthorized")
}

// exchangeRegistryAuth exchanges the long-lived Gordon token for a short-lived
// registry access token. Returns the Authorization header value (e.g. "Bearer <token>").
func (d *dockerImageOps) exchangeRegistryAuth(ctx context.Context) (string, error) {
	// Step 1: Get subject from server (validates token is still active)
	verifyResp, err := d.remoteClient.VerifyAuth(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to verify token: %w", err)
	}
	if !verifyResp.Valid {
		return "", fmt.Errorf("token is not valid")
	}
	if verifyResp.Subject == "" {
		return "", fmt.Errorf("empty subject in verified token")
	}

	// Step 2: Exchange long-lived token for short-lived registry token
	shortToken, err := d.remoteClient.ExchangeRegistryToken(ctx, verifyResp.Subject)
	if err != nil {
		return "", fmt.Errorf("failed to exchange registry token: %w", err)
	}

	return "Bearer " + shortToken, nil
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
