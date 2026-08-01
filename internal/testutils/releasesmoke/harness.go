package releasesmoke

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// Harness runs release artifact smoke checks previously encoded in the Makefile.
type Harness struct {
	DistDir         string
	Docker          CommandRunner
	Podman          CommandRunner
	HostArch        string
	DockerSocket    string
	ControlUser     string
	ControlUserns   string
	dockerSocketErr error
}

// NewHarness constructs a harness with default engine runners.
func NewHarness(distDir string) *Harness {
	socket := os.Getenv("DOCKER_HOST")
	var socketErr error
	switch {
	case socket == "":
		socket = "/var/run/docker.sock"
	case strings.HasPrefix(socket, "unix://"):
		socket = strings.TrimPrefix(socket, "unix://")
	default:
		socket = ""
		socketErr = fmt.Errorf("DOCKER_HOST must use a unix:// socket for the release image smoke")
	}
	control, _ := domain.FixedComponentProcessIdentity(domain.ComponentRoleControl)
	return &Harness{
		DistDir:         distDir,
		Docker:          DockerRunner(),
		Podman:          PodmanRunner(),
		HostArch:        runtime.GOARCH,
		DockerSocket:    socket,
		ControlUser:     control.User,
		ControlUserns:   fmt.Sprintf("keep-id:uid=%d,gid=%d", control.UID, control.GID),
		dockerSocketErr: socketErr,
	}
}

func (h *Harness) artifactsPath() string {
	return filepath.Join(h.DistDir, "artifacts.json")
}

func dockerImageSmokeServeRoleChecks(ctx context.Context, runner CommandRunner, image, arch string) error {
	for _, spec := range RoleIdentities {
		role := spec.Role
		if err := runQuiet(ctx, runner, "run", "--rm", "--platform", "linux/"+arch, image, "serve", "--role", string(role), "--help"); err != nil {
			return fmt.Errorf("%s serve --role %s: %w", arch, role, err)
		}
	}
	return nil
}

// RunImageSmoke verifies artifact-derived amd64/arm64 images under Docker/QEMU.
func (h *Harness) RunImageSmoke(ctx context.Context) error {
	if h.dockerSocketErr != nil {
		return h.dockerSocketErr
	}
	info, err := os.Stat(h.DockerSocket)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("docker socket %q is not available", h.DockerSocket)
	}
	smokeConfig, secretsConfig, err := h.writeTempConfigs()
	if err != nil {
		return err
	}
	defer os.Remove(smokeConfig)
	defer os.Remove(secretsConfig)

	for _, arch := range ImageArchitectures {
		image, err := ImageForArch(h.artifactsPath(), arch)
		if err != nil {
			return err
		}
		if err := h.verifyDockerImage(ctx, image, arch); err != nil {
			return fmt.Errorf("%s: %w", arch, err)
		}
		if err := h.dockerManagedPassLease(ctx, image, arch, secretsConfig); err != nil {
			return fmt.Errorf("%s managed pass: %w", arch, err)
		}
		if err := dockerImageSmokeServeRoleChecks(ctx, h.Docker, image, arch); err != nil {
			return err
		}
		if err := h.dockerMonolithProbe(ctx, image, arch, smokeConfig); err != nil {
			return fmt.Errorf("%s monolith: %w", arch, err)
		}
	}
	return nil
}

// RunPodmanManagedPassSmoke validates the host-architecture artifact under rootless Podman.
func (h *Harness) RunPodmanManagedPassSmoke(ctx context.Context) error {
	if err := requireRootlessPodman(ctx, h.Podman); err != nil {
		return err
	}
	image, err := ImageForArch(h.artifactsPath(), h.HostArch)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "gordon-release-podman-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	configPath := filepath.Join(tmp, "gordon.toml")
	if err := os.WriteFile(configPath, []byte("[auth]\nenabled = false\nsecrets_backend = \"pass\"\n"), 0o644); err != nil { //nolint:gosec // release smoke config is intentionally world-readable like the Makefile recipe.
		return err
	}

	if err := verifyEngineImageArch(ctx, h.Docker, image, h.HostArch); err != nil {
		return fmt.Errorf("docker image: %w", err)
	}
	if err := h.transferImageToPodman(ctx, tmp, image); err != nil {
		return err
	}
	if err := verifyEngineImageArch(ctx, h.Podman, image, h.HostArch); err != nil {
		return fmt.Errorf("podman image: %w", err)
	}
	alpineVersion, err := runOutput(ctx, h.Podman, "run", "--rm", "--entrypoint", "sh", image, "-ec", `test "$(id -u)" -ne 0; cat /etc/alpine-release`)
	if err != nil {
		return fmt.Errorf("non-root alpine gate: %w", err)
	}
	alpineVersion = strings.TrimSpace(alpineVersion)
	if !strings.HasPrefix(alpineVersion, "3.24.") {
		return fmt.Errorf("alpine version gate: expected 3.24, detected %q", alpineVersion)
	}

	cleanup, err := h.podmanRoleInspection(ctx, tmp, image)
	if err != nil {
		return err
	}
	defer cleanup()

	volume := fmt.Sprintf("gordon-release-podman-secrets-%d", os.Getpid())
	if err := runQuiet(ctx, h.Podman, "volume", "create", volume); err != nil {
		return fmt.Errorf("create secrets volume: %w", err)
	}
	defer func() { _ = runQuiet(context.Background(), h.Podman, "volume", "rm", "-f", volume) }()

	if err := h.podmanManagedPassLease(ctx, image, configPath, volume); err != nil {
		return err
	}
	return nil
}

func verifyEngineImageArch(ctx context.Context, runner CommandRunner, image, arch string) error {
	if err := runQuiet(ctx, runner, "image", "inspect", image); err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	got, err := runOutput(ctx, runner, "image", "inspect", "--format", "{{.Architecture}}", image)
	if err != nil {
		return err
	}
	if strings.TrimSpace(got) != arch {
		return fmt.Errorf("architecture %q != host %q", strings.TrimSpace(got), arch)
	}
	return nil
}

func (h *Harness) transferImageToPodman(ctx context.Context, tmp, image string) error {
	archive := filepath.Join(tmp, "artifact.tar")
	if err := runQuiet(ctx, h.Docker, "save", "-o", archive, image); err != nil {
		return fmt.Errorf("docker save: %w", err)
	}
	if err := runQuiet(ctx, h.Podman, "load", "-i", archive); err != nil {
		return fmt.Errorf("podman load: %w", err)
	}
	return nil
}

func (h *Harness) writeTempConfigs() (smokeConfig, secretsConfig string, err error) {
	defer func() {
		if err != nil {
			_ = os.Remove(smokeConfig)
			_ = os.Remove(secretsConfig)
			smokeConfig, secretsConfig = "", ""
		}
	}()

	smokeFile, err := os.CreateTemp("", "gordon-release-smoke-*.toml")
	if err != nil {
		return "", "", err
	}
	smokeConfig = smokeFile.Name()
	smokeContents := `[server]
runtime = "docker"
port = 8088
data_dir = "/tmp/gordon"
[auth]
enabled = false
secrets_backend = "unsafe"
`
	if _, err = smokeFile.WriteString(smokeContents); err != nil {
		_ = smokeFile.Close()
		return smokeConfig, "", err
	}
	if err = smokeFile.Close(); err != nil {
		return smokeConfig, "", err
	}
	if err = os.Chmod(smokeConfig, 0o644); err != nil { //nolint:gosec // matches prior release-image-smoke recipe permissions.
		return smokeConfig, "", err
	}

	secretsFile, err := os.CreateTemp("", "gordon-release-secrets-*.toml")
	if err != nil {
		return smokeConfig, "", err
	}
	secretsConfig = secretsFile.Name()
	if _, err = secretsFile.WriteString("[auth]\nenabled = false\nsecrets_backend = \"pass\"\n"); err != nil {
		_ = secretsFile.Close()
		return smokeConfig, secretsConfig, err
	}
	if err = secretsFile.Close(); err != nil {
		return smokeConfig, secretsConfig, err
	}
	if err = os.Chmod(secretsConfig, 0o644); err != nil { //nolint:gosec // matches prior release-image-smoke recipe permissions.
		return smokeConfig, secretsConfig, err
	}
	return smokeConfig, secretsConfig, nil
}

func (h *Harness) verifyDockerImage(ctx context.Context, image, arch string) error {
	if err := runQuiet(ctx, h.Docker, "image", "inspect", image); err != nil {
		return err
	}
	got, err := runOutput(ctx, h.Docker, "image", "inspect", "--format", "{{.Architecture}}", image)
	if err != nil {
		return err
	}
	if strings.TrimSpace(got) != arch {
		return fmt.Errorf("image architecture %q != %q", strings.TrimSpace(got), arch)
	}
	entrypoint, err := runOutput(ctx, h.Docker, "image", "inspect", "--format", "{{json .Config.Entrypoint}}", image)
	if err != nil {
		return err
	}
	if strings.TrimSpace(entrypoint) != `["/app/gordon"]` {
		return fmt.Errorf("unexpected entrypoint %s", strings.TrimSpace(entrypoint))
	}
	platform := "linux/" + arch
	for _, spec := range []struct {
		name string
		args []string
	}{
		{"help", []string{"run", "--rm", "--platform", platform, image, "--help"}},
		{"pass", []string{"run", "--rm", "--platform", platform, "--entrypoint", "pass", image, "version"}},
		{"gpg", []string{"run", "--rm", "--platform", platform, "--entrypoint", "gpg", image, "--version"}},
	} {
		if err := runQuiet(ctx, h.Docker, spec.args...); err != nil {
			return fmt.Errorf("%s: %w", spec.name, err)
		}
	}
	permCheck := fmt.Sprintf(`test "$(stat -c %%u:%%g:%%a /var/lib/gordon)" = 0:0:755; test "$(stat -c %%u:%%g:%%a /var/lib/gordon/secrets)" = %s:700`, h.ControlUser)
	if err := runQuiet(ctx, h.Docker, "run", "--rm", "--platform", platform, "--user", "0:0", "--entrypoint", "sh", image, "-ec", permCheck); err != nil {
		return fmt.Errorf("permission layout: %w", err)
	}
	return nil
}

func (h *Harness) dockerManagedPassLease(ctx context.Context, image, arch, secretsConfig string) error {
	volume := fmt.Sprintf("gordon-release-smoke-secrets-%s-%d", arch, os.Getpid())
	if err := runQuiet(ctx, h.Docker, "volume", "create", volume); err != nil {
		return err
	}
	defer func() { _ = runQuiet(context.Background(), h.Docker, "volume", "rm", "-f", volume) }()

	owner := fmt.Sprintf("gordon-release-smoke-secrets-owner-%s-%d", arch, os.Getpid())

	ownerCmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--name", owner, // #nosec G204 -- release gate invokes docker with artifact-derived image names.
		"--platform", "linux/"+arch, "--user", h.ControlUser,
		"-v", volume+":/var/lib/gordon/secrets",
		"-v", secretsConfig+":/tmp/gordon.toml:ro",
		"-e", "GNUPGHOME=/var/lib/gordon/secrets/current/gnupg",
		"-e", "PASSWORD_STORE_DIR=/var/lib/gordon/secrets/current/password-store",
		image, "secrets", "lock", "--config", "/tmp/gordon.toml")
	readiness, terminateOwner, err := startManagedPassOwner(ctx, ownerCmd)
	cleanupOwner := managedPassOwnerCleanup(h.Docker, owner, terminateOwner)
	defer cleanupOwner()
	if err != nil {
		return err
	}

	if readiness != ManagedPassLockMessage {
		return fmt.Errorf("unexpected readiness %q", readiness)
	}

	doctorCmd := exec.CommandContext(ctx, "docker", "run", "--rm", // #nosec G204 -- release gate invokes docker with artifact-derived image names.
		"--platform", "linux/"+arch, "--user", h.ControlUser,
		"-v", volume+":/var/lib/gordon/secrets",
		"-v", secretsConfig+":/tmp/gordon.toml:ro",
		"-e", "GNUPGHOME=/var/lib/gordon/secrets/current/gnupg",
		"-e", "PASSWORD_STORE_DIR=/var/lib/gordon/secrets/current/password-store",
		image, "secrets", "doctor", "--config", "/tmp/gordon.toml")
	doctorOut, doctorErr := doctorCmd.CombinedOutput()
	if doctorErr == nil {
		return fmt.Errorf("expected lease conflict, doctor succeeded")
	}
	if !strings.Contains(string(doctorOut), LeaseConflictMessage) {
		return fmt.Errorf("expected %q in doctor output, got: %s", LeaseConflictMessage, string(doctorOut))
	}

	cleanupOwner()

	for range 2 {
		if err := runQuiet(ctx, h.Docker, "run", "--rm",
			"--platform", "linux/"+arch, "--user", h.ControlUser,
			"-v", volume+":/var/lib/gordon/secrets",
			"-v", secretsConfig+":/tmp/gordon.toml:ro",
			"-e", "GNUPGHOME=/var/lib/gordon/secrets/current/gnupg",
			"-e", "PASSWORD_STORE_DIR=/var/lib/gordon/secrets/current/password-store",
			image, "secrets", "doctor", "--config", "/tmp/gordon.toml", "--write-check"); err != nil {
			return fmt.Errorf("post-lease doctor: %w", err)
		}
	}
	if err := runQuiet(ctx, h.Docker, "run", "--rm",
		"--platform", "linux/"+arch, "--user", h.ControlUser,
		"-v", volume+":/var/lib/gordon/secrets:ro",
		"--entrypoint", "sh", image, "-ec", ManagedPassArtifactShellCheck); err != nil {
		return fmt.Errorf("artifact check: %w", err)
	}
	return nil
}

func (h *Harness) dockerMonolithProbe(ctx context.Context, image, arch, smokeConfig string) error {
	name := fmt.Sprintf("gordon-release-smoke-%s-monolith-%d", arch, os.Getpid())
	if err := runQuiet(ctx, h.Docker, "run", "--detach", "--rm", "--name", name,
		"--platform", "linux/"+arch, "--user", "0:0",
		"-v", h.DockerSocket+":/var/run/docker.sock",
		"-v", smokeConfig+":/tmp/gordon.toml:ro",
		image, "serve", "--role", "monolith", "--config", "/tmp/gordon.toml"); err != nil {
		return err
	}
	defer func() { _ = runQuiet(context.Background(), h.Docker, "rm", "-f", name) }()

	deadline := time.Now().Add(ReadinessTimeout)
	for time.Now().Before(deadline) {
		if err := runQuiet(ctx, h.Docker, "exec", name, "wget", "-q", "-T", "2", "-O", "/dev/null", "http://127.0.0.1:5000/v2/"); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("monolith registry probe canceled: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("monolith registry probe timed out")
}

func requireRootlessPodman(ctx context.Context, podman CommandRunner) error {
	if _, err := exec.LookPath("podman"); err != nil {
		return fmt.Errorf("PRODUCTION GATE BLOCKED: rootless Podman is required for the managed-pass artifact smoke: %w", err)
	}
	rootless, err := runOutput(ctx, podman, "info", "--format", "{{.Host.Security.Rootless}}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(rootless) != "true" {
		return fmt.Errorf("PRODUCTION GATE BLOCKED: Podman is not running rootless")
	}
	return nil
}
