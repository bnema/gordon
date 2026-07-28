package releasesmoke

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//nolint:gocyclo // release gate role inspection mirrors the former inline Makefile recipe.
func (h *Harness) podmanRoleInspection(ctx context.Context, tmp, image string) (cleanup func(), err error) {
	rolesDir := filepath.Join(tmp, "roles")
	if err := os.MkdirAll(rolesDir, 0o700); err != nil {
		return nil, err
	}
	socketPath := filepath.Join(tmp, "runtime.sock")
	serviceLog := filepath.Join(tmp, "podman-service.log")
	service := exec.CommandContext(ctx, "podman", "system", "service", "--time=0", "unix://"+socketPath) // #nosec G204
	logFile, err := os.Create(serviceLog)
	if err != nil {
		return nil, err
	}
	service.Stdout = logFile
	service.Stderr = logFile
	if err := service.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("start podman service: %w", err)
	}
	servicePID := service.Process.Pid

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if info, statErr := os.Stat(socketPath); statErr == nil && info.Mode()&os.ModeSocket != 0 {
			break
		}
		time.Sleep(time.Second)
	}
	info, statErr := os.Stat(socketPath)
	if statErr != nil || info.Mode()&os.ModeSocket == 0 {
		_ = service.Process.Kill()
		_, _ = service.Process.Wait()
		logFile.Close()
		return nil, fmt.Errorf("podman service socket was not published")
	}
	_ = os.Chmod(socketPath, 0o600)

	var roleNames []string
	var generationVolumes []string

	roleSpecs := []struct {
		role     string
		identity string
	}{
		{"runtime", "21001"},
		{"control", "21002"},
		{"edge", "21003"},
		{"registry", "21004"},
	}

	for _, spec := range roleSpecs {
		name := fmt.Sprintf("gordon-release-podman-%s-%d", spec.role, os.Getpid())
		roleNames = append(roleNames, name)
		rolePrivate := filepath.Join(rolesDir, spec.role)
		if err := os.MkdirAll(rolePrivate, 0o700); err != nil {
			return nil, err
		}
		mounts := []string{"-v", rolePrivate + ":/private"}
		var stateVolume string
		if spec.role != "edge" {
			stateVolume = fmt.Sprintf("gordon-release-podman-%s-%d-g1", spec.role, os.Getpid())
			generationVolumes = append(generationVolumes, stateVolume)
			if err := runQuiet(ctx, h.Podman, "volume", "create", stateVolume); err != nil {
				return nil, err
			}
			mounts = append(mounts, "-v", stateVolume+":/var/lib/gordon:U")
		}
		if spec.role == "runtime" {
			mounts = append(mounts, "-v", socketPath+":/run/gordon/runtime.sock:ro")
		}

		args := []string{
			"run", "--detach", "--name", name,
			"--user", spec.identity + ":" + spec.identity,
			"--userns", "keep-id:uid=" + spec.identity + ",gid=" + spec.identity,
			"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		}
		args = append(args, mounts...)
		args = append(args, "--entrypoint", "sh", image, "-ec", `trap "exit 0" TERM INT; while :; do sleep 1; done`)
		if err := runQuiet(ctx, h.Podman, args...); err != nil {
			return nil, fmt.Errorf("start role %s: %w", spec.role, err)
		}

		idOut, err := runOutput(ctx, h.Podman, "exec", name, "sh", "-ec", `printf "%s:%s" "$(id -u)" "$(id -g)"`)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(idOut) != spec.identity+":"+spec.identity {
			return nil, fmt.Errorf("role %s identity mismatch: %q", spec.role, strings.TrimSpace(idOut))
		}
		userOut, err := runOutput(ctx, h.Podman, "inspect", "--format", "{{.Config.User}}", name)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(userOut) != spec.identity+":"+spec.identity {
			return nil, fmt.Errorf("role %s config user mismatch", spec.role)
		}
		usernsOut, err := runOutput(ctx, h.Podman, "inspect", "--format", "{{.HostConfig.UsernsMode}}", name)
		if err != nil {
			return nil, err
		}
		expectedUserns := fmt.Sprintf("keep-id:uid=%s,gid=%s", spec.identity, spec.identity)
		if strings.TrimSpace(usernsOut) != expectedUserns {
			return nil, fmt.Errorf("role %s userns mismatch: %q", spec.role, strings.TrimSpace(usernsOut))
		}
		capDrop, err := runOutput(ctx, h.Podman, "inspect", "--format", "{{json .HostConfig.CapDrop}}", name)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(capDrop) != `["ALL"]` {
			return nil, fmt.Errorf("role %s cap drop mismatch", spec.role)
		}
		capAdd, err := runOutput(ctx, h.Podman, "inspect", "--format", "{{range .HostConfig.CapAdd}}{{.}}{{end}}", name)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(capAdd) != "" {
			return nil, fmt.Errorf("role %s must not add capabilities", spec.role)
		}
		secOpt, err := runOutput(ctx, h.Podman, "inspect", "--format", "{{json .HostConfig.SecurityOpt}}", name)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(secOpt, "no-new-privileges") {
			return nil, fmt.Errorf("role %s missing no-new-privileges", spec.role)
		}

		privateProbe := `probe=/private/.write-check; : >"$probe"; test -r "$probe"; rm "$probe"; test ! -e "$probe"`
		if err := runQuiet(ctx, h.Podman, "exec", name, "sh", "-ec", privateProbe); err != nil {
			return nil, fmt.Errorf("role %s private write check: %w", spec.role, err)
		}

		if stateVolume != "" {
			mountOut, err := runOutput(ctx, h.Podman, "inspect", "--format",
				`{{range .Mounts}}{{if eq .Destination "/var/lib/gordon"}}{{.Name}}:{{.Mode}}{{end}}{{end}}`, name)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(mountOut) != stateVolume+":U" {
				return nil, fmt.Errorf("role %s state mount mismatch: %q", spec.role, strings.TrimSpace(mountOut))
			}
			stateProbe := fmt.Sprintf(`test "$(stat -c "%%u:%%g" /var/lib/gordon)" = "%s:%s"; chmod 0700 /var/lib/gordon; test "$(stat -c "%%a" /var/lib/gordon)" = 700; probe=/var/lib/gordon/.identity-write-check; : >"$probe"; test -r "$probe"; rm "$probe"; test ! -e "$probe"`, spec.identity, spec.identity)
			if err := runQuiet(ctx, h.Podman, "exec", name, "sh", "-ec", stateProbe); err != nil {
				return nil, fmt.Errorf("role %s state write check: %w", spec.role, err)
			}
		}

		if spec.role == "runtime" {
			if err := runQuiet(ctx, h.Podman, "exec", name, "test", "-S", "/run/gordon/runtime.sock"); err != nil {
				return nil, fmt.Errorf("runtime socket mount: %w", err)
			}
		} else {
			mountsOut, err := runOutput(ctx, h.Podman, "inspect", "--format", "{{range .Mounts}}{{println .Destination}}{{end}}", name)
			if err != nil {
				return nil, err
			}
			for _, line := range strings.Split(strings.TrimSpace(mountsOut), "\n") {
				if strings.TrimSpace(line) == "/run/gordon/runtime.sock" {
					return nil, fmt.Errorf("role %s must not mount runtime socket", spec.role)
				}
			}
		}
	}

	for _, roleName := range roleNames {
		if err := runQuiet(ctx, h.Podman, "rm", "-f", roleName); err != nil {
			return nil, err
		}
	}
	for _, vol := range generationVolumes {
		if err := runQuiet(ctx, h.Podman, "volume", "rm", vol); err != nil {
			return nil, err
		}
	}
	_ = service.Process.Kill()
	_, _ = service.Process.Wait()
	logFile.Close()
	_ = servicePID

	return func() {}, nil
}

func (h *Harness) podmanManagedPassLease(ctx context.Context, image, configPath, volume string) error {
	doctorArgs := []string{
		"run", "--rm", "--user", "21002:21002",
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

	tmp, err := os.MkdirTemp("", "gordon-podman-lease-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	readyPath := filepath.Join(tmp, "ready")
	if err := mkfifo(readyPath); err != nil {
		return err
	}

	owner := fmt.Sprintf("gordon-release-podman-owner-%d", os.Getpid())
	ownerCmd := exec.CommandContext(ctx, "podman", "run", "--rm", "--name", owner, // #nosec G204 -- release gate invokes podman with artifact-derived image names.
		"--user", "21002:21002",
		"--userns", "keep-id:uid=21002,gid=21002",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"-v", volume+":/var/lib/gordon/secrets",
		"-v", configPath+":/tmp/gordon.toml:ro",
		"-e", "GNUPGHOME=/var/lib/gordon/secrets/current/gnupg",
		"-e", "PASSWORD_STORE_DIR=/var/lib/gordon/secrets/current/password-store",
		image, "secrets", "lock", "--config", "/tmp/gordon.toml")
	ownerOut, _ := os.OpenFile(readyPath, os.O_WRONLY, 0)
	ownerCmd.Stdout = ownerOut
	ownerCmd.Stderr = os.Stderr
	if err := ownerCmd.Start(); err != nil {
		return fmt.Errorf("start lease owner: %w", err)
	}

	readiness, err := waitManagedPassReadiness(readyPath, ReadinessPollAttempts)
	if err != nil {
		_ = ownerCmd.Process.Kill()
		_, _ = ownerCmd.Process.Wait()
		return err
	}
	if readiness != ManagedPassLockMessage {
		return fmt.Errorf("unexpected readiness %q", readiness)
	}

	doctorConflict := exec.CommandContext(ctx, "podman", "run", "--rm", // #nosec G204 -- release gate invokes podman with artifact-derived image names.
		"--user", "21002:21002",
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

	_ = runQuiet(ctx, h.Podman, "rm", "-f", owner)
	_ = ownerCmd.Process.Kill()
	_, _ = ownerCmd.Process.Wait()

	if err := runQuiet(ctx, h.Podman, doctorArgs...); err != nil {
		return fmt.Errorf("post-lease doctor: %w", err)
	}
	artifactCheck := `test -s /var/lib/gordon/secrets/current/.gordon-managed-pass-fingerprint; test -s /var/lib/gordon/secrets/current/password-store/.gpg-id; test -d /var/lib/gordon/secrets/current/gnupg`
	if err := runQuiet(ctx, h.Podman, "run", "--rm",
		"--user", "21002:21002",
		"--userns", "keep-id:uid=21002,gid=21002",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"-v", volume+":/var/lib/gordon/secrets:ro",
		"--entrypoint", "sh", image, "-ec", artifactCheck); err != nil {
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
