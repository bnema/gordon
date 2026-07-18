package docker

import (
	"context"
	"fmt"
	"strings"
	"syscall"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"

	"github.com/bnema/gordon/internal/boundaries/out"
)

var _ out.RuntimeEnvironmentProbe = (*Runtime)(nil)

// ProbeRuntimeEnvironment converts Docker-compatible daemon responses into the
// intentionally narrow migration contract. Daemon paths and raw metadata stay
// inside the runtime adapter.
func (r *Runtime) ProbeRuntimeEnvironment(ctx context.Context) (out.RuntimeEnvironment, error) {
	if r == nil || r.client == nil {
		return out.RuntimeEnvironment{}, fmt.Errorf("runtime client is not configured")
	}
	if _, err := r.client.Ping(ctx); err != nil {
		return out.RuntimeEnvironment{}, fmt.Errorf("ping runtime: %w", err)
	}
	info, err := r.client.Info(ctx)
	if err != nil {
		return out.RuntimeEnvironment{}, fmt.Errorf("inspect runtime: %w", err)
	}
	_, imageErr := r.client.ImageList(ctx, imageListOptions())
	_, networkErr := r.client.NetworkList(ctx, networkListOptions())
	available, sufficient := runtimeDiskCapacity(info.DockerRootDir)
	return out.RuntimeEnvironment{
		Engine:          runtimeEngineKind(info.Name, info.ServerVersion),
		Rootless:        runtimeIsRootless(info.SecurityOptions),
		APIReachable:    true,
		ImageAvailable:  imageErr == nil,
		ImagePullable:   imageErr == nil,
		NetworkFeasible: networkErr == nil,
		DiskAvailable:   available,
		DiskSufficient:  sufficient,
	}, nil
}

// Small helpers keep Docker API option types out of the sanitized boundary.
func imageListOptions() image.ListOptions     { return image.ListOptions{} }
func networkListOptions() network.ListOptions { return network.ListOptions{} }

func runtimeEngineKind(name, version string) string {
	if strings.Contains(strings.ToLower(name+" "+version), "podman") {
		return "podman"
	}
	return "docker"
}

func runtimeIsRootless(options []string) bool {
	for _, option := range options {
		if strings.Contains(strings.ToLower(option), "rootless") {
			return true
		}
	}
	return false
}

func runtimeDiskCapacity(root string) (uint64, bool) {
	var stat syscall.Statfs_t
	if root == "" || syscall.Statfs(root, &stat) != nil || stat.Bsize <= 0 {
		return 0, false
	}
	available := stat.Bavail * uint64(stat.Bsize)
	// 1 GiB leaves enough headroom for the component images and metadata.
	return available, available >= 1<<30
}
