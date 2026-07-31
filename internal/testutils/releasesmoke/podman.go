package releasesmoke

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

//nolint:gocyclo // release gate role inspection mirrors the former inline Makefile recipe.
func (h *Harness) podmanRoleInspection(ctx context.Context, tmp, image string) (cleanup func(), err error) {
	rolesDir := filepath.Join(tmp, "roles")
	if err := os.MkdirAll(rolesDir, 0o700); err != nil {
		return func() {}, err
	}
	socketPath := filepath.Join(tmp, "runtime.sock")
	serviceLog := filepath.Join(tmp, "podman-service.log")

	var (
		service           *exec.Cmd
		logFile           *os.File
		roleNames         []string
		generationVolumes []string
		cleanupOnce       sync.Once
	)

	cleanup = func() {
		cleanupOnce.Do(func() {
			bg, cancel := context.WithTimeout(context.Background(), ReadinessTimeout)
			defer cancel()
			for _, roleName := range roleNames {
				_ = runQuiet(bg, h.Podman, "rm", "-f", roleName)
			}
			for _, vol := range generationVolumes {
				_ = runQuiet(bg, h.Podman, "volume", "rm", "-f", vol)
			}
			if service != nil && service.Process != nil {
				_ = service.Process.Kill()
				_ = service.Wait()
			}
			if logFile != nil {
				_ = logFile.Close()
			}
			_ = os.Remove(socketPath)
			_ = os.Remove(serviceLog)
		})
	}

	service = h.Podman.Command(ctx, "system", "service", "--time=0", "unix://"+socketPath)
	logFile, err = os.Create(serviceLog)
	if err != nil {
		return cleanup, err
	}
	service.Stdout = logFile
	service.Stderr = logFile
	if err = service.Start(); err != nil {
		_ = logFile.Close()
		logFile = nil
		return cleanup, fmt.Errorf("start podman service: %w", err)
	}
	// Idempotent cleanup is installed immediately after service start.
	defer func() {
		if err != nil {
			cleanup()
		}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if info, statErr := os.Stat(socketPath); statErr == nil && info.Mode()&os.ModeSocket != 0 {
			break
		}
		select {
		case <-ctx.Done():
			return cleanup, fmt.Errorf("wait for podman service socket: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
	info, statErr := os.Stat(socketPath)
	if statErr != nil || info.Mode()&os.ModeSocket == 0 {
		return cleanup, fmt.Errorf("podman service socket was not published")
	}
	if err = os.Chmod(socketPath, 0o600); err != nil {
		return cleanup, fmt.Errorf("restrict podman service socket: %w", err)
	}

	for _, spec := range RoleIdentities {
		roleName := string(spec.Role)
		containerUser := spec.Identity + ":" + spec.Identity
		name := fmt.Sprintf("gordon-release-podman-%s-%d", roleName, os.Getpid())
		roleNames = append(roleNames, name)
		rolePrivate := filepath.Join(rolesDir, roleName)
		if err = os.MkdirAll(rolePrivate, 0o700); err != nil {
			return cleanup, err
		}
		mounts := []string{"-v", rolePrivate + ":/private"}
		var stateVolume string
		if spec.Role != domain.ComponentRoleEdge {
			stateVolume = fmt.Sprintf("gordon-release-podman-%s-%d-g1", roleName, os.Getpid())
			generationVolumes = append(generationVolumes, stateVolume)
			if err = runQuiet(ctx, h.Podman, "volume", "create", stateVolume); err != nil {
				return cleanup, err
			}
			mounts = append(mounts, "-v", stateVolume+":/var/lib/gordon:U")
		}
		if spec.Role == domain.ComponentRoleRuntime {
			mounts = append(mounts, "-v", socketPath+":/run/gordon/runtime.sock:ro")
		}

		args := []string{
			"run", "--detach", "--name", name,
			"--user", containerUser,
			"--userns", "keep-id:uid=" + spec.Identity + ",gid=" + spec.Identity,
			"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		}
		args = append(args, mounts...)
		args = append(args, "--entrypoint", "sh", image, "-ec", `trap "exit 0" TERM INT; while :; do sleep 1; done`)
		if err = runQuiet(ctx, h.Podman, args...); err != nil {
			return cleanup, fmt.Errorf("start role %s: %w", roleName, err)
		}

		idOut, err := runOutput(ctx, h.Podman, "exec", name, "sh", "-ec", `printf "%s:%s" "$(id -u)" "$(id -g)"`)
		if err != nil {
			return cleanup, err
		}
		if strings.TrimSpace(idOut) != containerUser {
			return cleanup, fmt.Errorf("role %s identity mismatch: %q", roleName, strings.TrimSpace(idOut))
		}
		userOut, err := runOutput(ctx, h.Podman, "inspect", "--format", "{{.Config.User}}", name)
		if err != nil {
			return cleanup, err
		}
		if strings.TrimSpace(userOut) != containerUser {
			return cleanup, fmt.Errorf("role %s config user mismatch", roleName)
		}
		usernsOut, err := runOutput(ctx, h.Podman, "inspect", "--format", "{{.HostConfig.UsernsMode}}", name)
		if err != nil {
			return cleanup, err
		}
		expectedUserns := fmt.Sprintf("keep-id:uid=%s,gid=%s", spec.Identity, spec.Identity)
		if strings.TrimSpace(usernsOut) != expectedUserns {
			return cleanup, fmt.Errorf("role %s userns mismatch: %q", roleName, strings.TrimSpace(usernsOut))
		}
		capDrop, err := runOutput(ctx, h.Podman, "inspect", "--format", "{{json .HostConfig.CapDrop}}", name)
		if err != nil {
			return cleanup, err
		}
		if strings.TrimSpace(capDrop) != `["ALL"]` {
			return cleanup, fmt.Errorf("role %s cap drop mismatch", roleName)
		}
		capAdd, err := runOutput(ctx, h.Podman, "inspect", "--format", "{{range .HostConfig.CapAdd}}{{.}}{{end}}", name)
		if err != nil {
			return cleanup, err
		}
		if strings.TrimSpace(capAdd) != "" {
			return cleanup, fmt.Errorf("role %s must not add capabilities", roleName)
		}
		secOpt, err := runOutput(ctx, h.Podman, "inspect", "--format", "{{json .HostConfig.SecurityOpt}}", name)
		if err != nil {
			return cleanup, err
		}
		if !strings.Contains(secOpt, "no-new-privileges") {
			return cleanup, fmt.Errorf("role %s missing no-new-privileges", roleName)
		}

		privateProbe := `probe=/private/.write-check; : >"$probe"; test -r "$probe"; rm "$probe"; test ! -e "$probe"`
		if err = runQuiet(ctx, h.Podman, "exec", name, "sh", "-ec", privateProbe); err != nil {
			return cleanup, fmt.Errorf("role %s private write check: %w", roleName, err)
		}

		mountModes, err := runOutput(ctx, h.Podman, "inspect", "--format", "{{range .Mounts}}{{println .Destination .Mode}}{{end}}", name)
		if err != nil {
			return cleanup, err
		}
		gordonProjection, err := runOutput(ctx, h.Podman, "inspect", "--format", "{{range .Mounts}}{{if eq .Destination \"/var/lib/gordon\"}}{{ .Name }}:{{ .Mode }}{{end}}{{end}}", name)
		if err != nil {
			return cleanup, err
		}
		bindsJSON, err := runOutput(ctx, h.Podman, "inspect", "--format", "{{json .HostConfig.Binds}}", name)
		if err != nil {
			return cleanup, err
		}
		if err = assertExclusiveGenerationVolumeU(roleName, stateVolume, mountModes, gordonProjection, bindsJSON); err != nil {
			return cleanup, err
		}

		if stateVolume != "" {
			stateProbe := fmt.Sprintf(`test "$(stat -c "%%u:%%g" /var/lib/gordon)" = "%s:%s"; chmod 0700 /var/lib/gordon; test "$(stat -c "%%a" /var/lib/gordon)" = 700; probe=/var/lib/gordon/.identity-write-check; : >"$probe"; test -r "$probe"; rm "$probe"; test ! -e "$probe"`, spec.Identity, spec.Identity)
			if err = runQuiet(ctx, h.Podman, "exec", name, "sh", "-ec", stateProbe); err != nil {
				return cleanup, fmt.Errorf("role %s state write check: %w", roleName, err)
			}
		}

		if spec.Role == domain.ComponentRoleRuntime {
			if err = runQuiet(ctx, h.Podman, "exec", name, "test", "-S", "/run/gordon/runtime.sock"); err != nil {
				return cleanup, fmt.Errorf("runtime socket mount: %w", err)
			}
		} else {
			mountsOut, err := runOutput(ctx, h.Podman, "inspect", "--format", "{{range .Mounts}}{{println .Destination}}{{end}}", name)
			if err != nil {
				return cleanup, err
			}
			for _, line := range strings.Split(strings.TrimSpace(mountsOut), "\n") {
				if strings.TrimSpace(line) == "/run/gordon/runtime.sock" {
					return cleanup, fmt.Errorf("role %s must not mount runtime socket", roleName)
				}
			}
		}
	}

	for _, roleName := range roleNames {
		if err = runQuiet(ctx, h.Podman, "rm", "-f", roleName); err != nil {
			return cleanup, err
		}
	}
	for _, vol := range generationVolumes {
		if err = runQuiet(ctx, h.Podman, "volume", "rm", vol); err != nil {
			return cleanup, err
		}
	}
	if err = assertResourcesUninspectable(ctx, h.Podman, roleNames, generationVolumes); err != nil {
		return cleanup, err
	}

	cleanup()
	return func() {}, nil
}

// assertExclusiveGenerationVolumeU requires exact U only on non-edge /var/lib/gordon.
func assertExclusiveGenerationVolumeU(role, stateVolume, mountModes, gordonProjection, bindsJSON string) error {
	if role == string(domain.ComponentRoleEdge) {
		if err := assertEdgeHasNoGenerationMountU(mountModes, gordonProjection); err != nil {
			return err
		}
		return assertEdgeHasNoGenerationVolumeU(bindsJSON)
	}
	if err := assertNonEdgeHasExactGenerationMountProjection(role, stateVolume, mountModes, gordonProjection); err != nil {
		return err
	}
	return assertNonEdgeHasExactGenerationVolumeBind(role, stateVolume, bindsJSON)
}

func assertNonEdgeHasExactGenerationMountProjection(role, stateVolume, mountModes, gordonProjection string) error {
	actualProjection := strings.TrimSpace(gordonProjection)
	name, _, _ := strings.Cut(actualProjection, ":")
	if name != stateVolume {
		return fmt.Errorf("role %s /var/lib/gordon mount projection must use %s, got %q", role, stateVolume, actualProjection)
	}
	for _, line := range strings.Split(strings.TrimSpace(mountModes), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "/var/lib/gordon" {
			return nil
		}
	}
	return fmt.Errorf("role %s must mount /var/lib/gordon", role)
}

func assertEdgeHasNoGenerationMountU(_ string, gordonProjection string) error {
	if strings.TrimSpace(gordonProjection) != "" {
		return fmt.Errorf("edge must not mount /var/lib/gordon")
	}
	return nil
}

func assertEdgeHasNoGenerationVolumeU(bindsJSON string) error {
	binds, err := parseJSONStringList(bindsJSON)
	if err != nil {
		return err
	}
	for _, bind := range binds {
		parts := strings.Split(bind, ":")
		if len(parts) >= 2 && filepath.Clean(parts[1]) == "/var/lib/gordon" {
			return fmt.Errorf("edge must not mount /var/lib/gordon")
		}
		if len(parts) >= 3 && bindModeHasUToken(parts[2]) {
			return fmt.Errorf("edge must not have a U bind: %q", bind)
		}
	}
	return nil
}

func assertNonEdgeHasExactGenerationVolumeBind(role, stateVolume, bindsJSON string) error {
	binds, err := parseJSONStringList(bindsJSON)
	if err != nil {
		return err
	}
	var generationBinds []string
	for _, bind := range binds {
		parts := strings.Split(bind, ":")
		if len(parts) < 2 || filepath.Clean(parts[1]) != "/var/lib/gordon" {
			if len(parts) >= 3 && bindModeHasUToken(parts[2]) {
				return fmt.Errorf("role %s must not apply U outside its generation volume: %q", role, bind)
			}
			continue
		}
		generationBinds = append(generationBinds, bind)
	}
	if len(generationBinds) != 1 {
		return fmt.Errorf("role %s must have exactly one /var/lib/gordon bind, got %d (binds=%s)", role, len(generationBinds), bindsJSON)
	}
	parts := strings.Split(generationBinds[0], ":")
	if len(parts) != 3 || parts[0] != stateVolume || !validReleaseSmokeChownMode(parts[2]) {
		return fmt.Errorf("role %s /var/lib/gordon bind must use volume %q with U, got %q", role, stateVolume, generationBinds[0])
	}
	return nil
}

func validReleaseSmokeChownMode(mode string) bool {
	seenU := false
	for _, option := range strings.Split(mode, ",") {
		switch strings.TrimSpace(option) {
		case domain.ContainerVolumeOptionChown:
			if seenU {
				return false
			}
			seenU = true
		case "private", "rprivate":
		default:
			return false
		}
	}
	return seenU
}

func bindModeHasUToken(mode string) bool {
	for _, option := range strings.Split(mode, ",") {
		if strings.TrimSpace(option) == domain.ContainerVolumeOptionChown {
			return true
		}
	}
	return false
}

func parseJSONStringList(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("parse inspected string list: %w", err)
	}
	return out, nil
}

func assertResourcesUninspectable(ctx context.Context, runner CommandRunner, containers, volumes []string) error {
	for _, name := range containers {
		if _, err := runOutput(ctx, runner, "container", "inspect", name); err == nil {
			return fmt.Errorf("role container %q must be uninspectable after removal", name)
		}
	}
	for _, name := range volumes {
		if _, err := runOutput(ctx, runner, "volume", "inspect", name); err == nil {
			return fmt.Errorf("generation volume %q must be uninspectable after removal", name)
		}
	}
	return nil
}

func (h *Harness) podmanManagedPassLease(ctx context.Context, image, configPath, volume string) error {
	doctorArgs := []string{
		"run", "--rm", "--user", h.ControlUser,
		"--userns", "keep-id:uid=21002,gid=21002",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"-v", volume + ":/var/lib/gordon/secrets",
		"-v", configPath + ":/tmp/gordon.toml:ro",
		"-e", "GNUPGHOME=/var/lib/gordon/secrets/current/gnupg",
		"-e", "PASSWORD_STORE_DIR=/var/lib/gordon/secrets/current/password-store",
		image, "secrets", "doctor", "--config", "/tmp/gordon.toml", "--write-check",
	}
	if err := runQuiet(ctx, h.Podman, doctorArgs...); err != nil {
		return fmt.Errorf("initial doctor: %w", err)
	}

	owner := fmt.Sprintf("gordon-release-podman-owner-%d", os.Getpid())
	ownerCmd := h.Podman.Command(ctx, "run", "--rm", "--name", owner,
		"--user", h.ControlUser,
		"--userns", "keep-id:uid=21002,gid=21002",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"-v", volume+":/var/lib/gordon/secrets",
		"-v", configPath+":/tmp/gordon.toml:ro",
		"-e", "GNUPGHOME=/var/lib/gordon/secrets/current/gnupg",
		"-e", "PASSWORD_STORE_DIR=/var/lib/gordon/secrets/current/password-store",
		image, "secrets", "lock", "--config", "/tmp/gordon.toml")
	readiness, terminateOwner, err := startManagedPassOwner(ctx, ownerCmd)
	cleanupOwner := managedPassOwnerCleanup(h.Podman, owner, terminateOwner)
	defer cleanupOwner()
	if err != nil {
		return err
	}

	if readiness != ManagedPassLockMessage {
		return fmt.Errorf("unexpected readiness %q", readiness)
	}

	doctorConflict := h.Podman.Command(ctx, "run", "--rm",
		"--user", h.ControlUser,
		"--userns", "keep-id:uid=21002,gid=21002",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"-v", volume+":/var/lib/gordon/secrets",
		"-v", configPath+":/tmp/gordon.toml:ro",
		"-e", "GNUPGHOME=/var/lib/gordon/secrets/current/gnupg",
		"-e", "PASSWORD_STORE_DIR=/var/lib/gordon/secrets/current/password-store",
		image, "secrets", "doctor", "--config", "/tmp/gordon.toml")
	conflictOut, conflictErr := doctorConflict.CombinedOutput()
	if conflictErr == nil {
		return fmt.Errorf("expected lease conflict, doctor succeeded")
	}
	if !strings.Contains(string(conflictOut), LeaseConflictMessage) {
		return fmt.Errorf("expected %q in doctor output", LeaseConflictMessage)
	}

	cleanupOwner()

	if err := runQuiet(ctx, h.Podman, doctorArgs...); err != nil {
		return fmt.Errorf("post-lease doctor: %w", err)
	}
	if err := runQuiet(ctx, h.Podman, "run", "--rm",
		"--user", h.ControlUser,
		"--userns", "keep-id:uid=21002,gid=21002",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"-v", volume+":/var/lib/gordon/secrets:ro",
		"--entrypoint", "sh", image, "-ec", ManagedPassArtifactShellCheck); err != nil {
		return fmt.Errorf("artifact check: %w", err)
	}
	if err := runQuiet(ctx, h.Podman, "volume", "rm", volume); err != nil {
		return fmt.Errorf("volume cleanup: %w", err)
	}
	if _, err := runOutput(ctx, h.Podman, "volume", "inspect", volume); err == nil {
		return fmt.Errorf("volume %q must be removed after smoke", volume)
	}
	return nil
}
