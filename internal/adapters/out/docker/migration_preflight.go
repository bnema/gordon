package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

var _ out.RuntimeEnvironmentProbe = (*Runtime)(nil)
var _ out.RuntimePublicListenerProbe = (*Runtime)(nil)

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
	version, err := r.runtimeVersion(ctx)
	if err != nil {
		return out.RuntimeEnvironment{}, fmt.Errorf("inspect runtime version: %w", err)
	}
	info, err := r.runtimeInfo(ctx)
	if err != nil {
		return out.RuntimeEnvironment{}, fmt.Errorf("inspect runtime: %w", err)
	}
	_, imageErr := r.client.ImageList(ctx, imageListOptions())
	_, networkErr := r.client.NetworkList(ctx, networkListOptions())
	available, sufficient := runtimeDiskCapacity(info.DockerRootDir)
	return out.RuntimeEnvironment{
		Engine:          runtimeEngineKind(version.Components),
		Rootless:        runtimeIsRootless(info.Rootless, info.SecurityOptions),
		APIReachable:    true,
		ImageAvailable:  imageErr == nil,
		ImagePullable:   imageErr == nil,
		NetworkFeasible: networkErr == nil,
		DiskAvailable:   available,
		DiskSufficient:  sufficient,
	}, nil
}

// ProbePublicListeners returns one sanitized availability boolean per port.
// A port is accepted only when it is bindable or held by a running, labelled
// Gordon monolith; all ambiguous owners fail closed. Runtime checks the engine
// again after each free-port bind probe so a container bind race is rejected.
func (r *Runtime) ProbePublicListeners(ctx context.Context, ports []int) ([]bool, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("runtime client is not configured")
	}
	available := make([]bool, len(ports))
	for i, port := range ports {
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid public listener")
		}
		owner, err := r.publicListenerOwner(ctx, port)
		if err != nil {
			return nil, fmt.Errorf("inspect public listener: %w", err)
		}
		if owner.occupied {
			available[i] = owner.managedMonolith
			continue
		}
		if !runtimePortBindable(port) {
			continue
		}
		// Do not accept a port whose engine ownership changed while the bind
		// probe ran. Activation performs the final atomic engine bind.
		owner, err = r.publicListenerOwner(ctx, port)
		if err != nil {
			return nil, fmt.Errorf("recheck public listener: %w", err)
		}
		available[i] = !owner.occupied
	}
	return available, nil
}

type publicListenerOwner struct {
	occupied        bool
	managedMonolith bool
}

func (r *Runtime) publicListenerOwner(ctx context.Context, wanted int) (publicListenerOwner, error) {
	containers, err := r.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return publicListenerOwner{}, err
	}
	owner := publicListenerOwner{}
	for _, candidate := range containers {
		// A legacy monolith may own its listener through host networking rather
		// than a published-port record. It is still an unambiguous Gordon owner
		// when its managed identity is present; rejecting it would make the
		// required in-place migration impossible before any mutation occurs.
		if candidate.HostConfig.NetworkMode == "host" && runningManagedMonolith(candidate) {
			owner.occupied = true
			owner.managedMonolith = true
			continue
		}
		for _, binding := range candidate.Ports {
			if int(binding.PublicPort) != wanted {
				continue
			}
			owner.occupied = true
			if !runningManagedMonolith(candidate) {
				return owner, nil
			}
			owner.managedMonolith = true
		}
	}
	return owner, nil
}

func runningManagedMonolith(candidate container.Summary) bool {
	if candidate.State != "running" || candidate.Labels[domain.LabelManaged] != "true" || candidate.Labels[domain.LabelComponent] == "true" {
		return false
	}
	for _, name := range candidate.Names {
		switch strings.TrimPrefix(name, "/") {
		case "gordon", "gordon-monolith", "monolith":
			return true
		}
	}
	return false
}

func runtimePortBindable(port int) bool {
	listener, err := net.Listen("tcp", net.JoinHostPort("", fmt.Sprintf("%d", port)))
	if err != nil {
		return false
	}
	return listener.Close() == nil
}

// Small helpers keep Docker API option types out of the sanitized boundary.
func imageListOptions() image.ListOptions     { return image.ListOptions{} }
func networkListOptions() network.ListOptions { return network.ListOptions{} }

const maxRuntimeMetadataResponse = 1 << 20

type runtimeVersionResponse struct {
	Components []struct {
		Name string `json:"Name"`
	} `json:"Components"`
}

type runtimeInfoResponse struct {
	DockerRootDir   string   `json:"DockerRootDir"`
	Rootless        *bool    `json:"Rootless"`
	SecurityOptions []string `json:"SecurityOptions"`
}

func (r *Runtime) runtimeVersion(ctx context.Context) (runtimeVersionResponse, error) {
	var version runtimeVersionResponse
	return version, r.decodeRuntimeMetadata(ctx, "/version", &version)
}

func (r *Runtime) runtimeInfo(ctx context.Context) (runtimeInfoResponse, error) {
	var info runtimeInfoResponse
	return info, r.decodeRuntimeMetadata(ctx, "/v"+r.client.ClientVersion()+"/info", &info)
}

// decodeRuntimeMetadata uses the adapter's negotiated transport while bounding
// daemon-controlled payloads. It intentionally decodes only facts used by the
// sanitized migration boundary rather than forwarding a raw engine response.
func (r *Runtime) decodeRuntimeMetadata(ctx context.Context, path string, target any) error {
	endpoint, err := url.Parse(r.client.DaemonHost())
	if err != nil {
		return err
	}
	switch endpoint.Scheme {
	case "tcp":
		endpoint.Scheme = "http"
	case "unix":
		// Docker's configured Unix transport ignores the HTTP authority.
		endpoint.Scheme, endpoint.Host = "http", "docker"
	}
	endpoint.Path = path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	resp, err := r.client.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("runtime metadata request failed")
	}
	limited := io.LimitReader(resp.Body, maxRuntimeMetadataResponse)
	if err := json.NewDecoder(limited).Decode(target); err != nil {
		return err
	}
	return nil
}

// runtimeEngineKind accepts Podman only when its Docker-compatible /version
// response names a Podman Engine component. Host OS, architecture, daemon name,
// and free-form version strings are deliberately not engine evidence.
func runtimeEngineKind(components []struct {
	Name string `json:"Name"`
}) string {
	podman, conflicting := false, false
	for _, component := range components {
		switch strings.ToLower(strings.TrimSpace(component.Name)) {
		case "podman engine":
			podman = true
		case "docker engine":
			conflicting = true
		}
	}
	if podman && !conflicting {
		return "podman"
	}
	return "docker"
}

// runtimeIsRootless recognizes only Podman's structured Rootless fact or its
// exact SecurityOptions marker. An explicitly false Rootless fact conflicts
// with the marker and is rejected rather than guessed.
func runtimeIsRootless(rootless *bool, options []string) bool {
	marker := false
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option), "name=rootless") {
			marker = true
			break
		}
	}
	if rootless != nil && !*rootless && marker {
		return false
	}
	return marker || (rootless != nil && *rootless)
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
