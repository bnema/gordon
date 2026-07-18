package compatoldnew

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type PodmanResource struct {
	ID     string            `json:"Id"`
	Name   string            `json:"Name"`
	Names  []string          `json:"Names"`
	Labels map[string]string `json:"Labels"`
}

func (r PodmanResource) resourceName() string {
	if r.Name != "" {
		return r.Name
	}
	if len(r.Names) == 1 {
		return r.Names[0]
	}
	return ""
}

type podmanInfo struct {
	Host struct {
		Security struct {
			Rootless bool `json:"rootless"`
		} `json:"security"`
	} `json:"host"`
}

// PodmanRootless verifies the target engine from its JSON stdout. Stderr is
// captured only for failures because Podman can emit benign cgroup warnings
// there even when it reports a rootless engine successfully.
func PodmanRootless(ctx context.Context) (bool, error) {
	if _, err := exec.LookPath("podman"); err != nil {
		return false, fmt.Errorf("podman unavailable: binary not found in PATH: %w", err)
	}
	cmd, err := newIsolatedCommand(ctx, "podman", []string{"info", "--format", "json"}, nil, nil, false)
	if err != nil {
		return false, fmt.Errorf("prepare podman info: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("podman info failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var info podmanInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return false, fmt.Errorf("decode podman info JSON: %w", err)
	}
	return info.Host.Security.Rootless, nil
}

func PodmanAvailable(ctx context.Context) error {
	if _, err := exec.LookPath("podman"); err != nil {
		return fmt.Errorf("podman unavailable: binary not found in PATH: %w", err)
	}
	cmd, err := newIsolatedCommand(ctx, "podman", []string{"info", "--format", "{{.Host.OCIRuntime.Name}}"}, nil, nil, false)
	if err != nil {
		return fmt.Errorf("podman unavailable: prepare podman info: %w", err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("podman unavailable: podman info failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func RequirePodman(t interface {
	Helper()
	Skipf(string, ...any)
}) {
	t.Helper()
	if err := PodmanAvailable(context.Background()); err != nil {
		t.Skipf("Podman smoke test skipped: %v", err)
	}
}

func InspectContainers(ctx context.Context, runID string) ([]PodmanResource, error) {
	return inspect(ctx, "ps", []string{"-a"}, runID)
}

func InspectNetworks(ctx context.Context, runID string) ([]PodmanResource, error) {
	return inspect(ctx, "network", []string{"ls"}, runID)
}

func InspectVolumes(ctx context.Context, runID string) ([]PodmanResource, error) {
	return inspect(ctx, "volume", []string{"ls"}, runID)
}

func CleanupRunResources(ctx context.Context, runID string) error {
	if runID == "" || sanitizePart(runID) == "" {
		return errors.New("cleanup requires non-empty runID")
	}
	containers, err := InspectContainers(ctx, runID)
	if err != nil {
		return err
	}
	for _, r := range containers {
		if isHarnessResource(r, runID) {
			if err := podman(ctx, "rm", "-f", r.ID); err != nil {
				return err
			}
		}
	}
	for _, kind := range []struct {
		name, rm string
		inspect  func(context.Context, string) ([]PodmanResource, error)
	}{
		{"network", "network", InspectNetworks},
		{"volume", "volume", InspectVolumes},
	} {
		resources, err := kind.inspect(ctx, runID)
		if err != nil {
			return err
		}
		for _, r := range resources {
			if isHarnessResource(r, runID) {
				name := r.resourceName()
				if err := podman(ctx, kind.rm, "rm", name); err != nil {
					return fmt.Errorf("remove %s %s: %w", kind.name, name, err)
				}
			}
		}
	}
	return nil
}

func inspect(ctx context.Context, command string, base []string, runID string) ([]PodmanResource, error) {
	args := append([]string{command}, base...)
	args = append(args, "--filter", "label="+LabelRun+"="+sanitizePart(runID), "--format", "json")
	out, err := podmanOutput(ctx, args...)
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" || out == "null" {
		return nil, nil
	}
	var resources []PodmanResource
	if err := json.Unmarshal([]byte(out), &resources); err != nil {
		return nil, fmt.Errorf("decode podman %s output: %w", command, err)
	}
	return resources, nil
}

func isHarnessResource(r PodmanResource, runID string) bool {
	labels := r.Labels
	if labels[LabelRun] != sanitizePart(runID) || labels[LabelSide] == "" || labels[LabelFixture] == "" {
		return false
	}
	name := r.resourceName()
	return strings.HasPrefix(name, ContainerPrefix(runID, labels[LabelSide])) ||
		strings.HasPrefix(name, NetworkPrefix(runID, labels[LabelSide])) ||
		strings.HasPrefix(name, VolumePrefix(runID, labels[LabelSide]))
}

func podman(ctx context.Context, args ...string) error {
	_, err := podmanOutput(ctx, args...)
	return err
}

func podmanOutput(ctx context.Context, args ...string) (string, error) {
	if _, err := exec.LookPath("podman"); err != nil {
		return "", fmt.Errorf("podman unavailable: binary not found in PATH: %w", err)
	}
	cmd, err := newIsolatedCommand(ctx, "podman", args, nil, nil, false)
	if err != nil {
		return "", fmt.Errorf("prepare podman command: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("podman %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}
