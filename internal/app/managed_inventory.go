package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// ManagedActualStateInventory is the read-only runtime boundary needed by
// migration preflight. It deliberately accepts only the compact actual-state
// types below; callers cannot obtain raw runtime inspect data through it.
type ManagedActualStateInventory interface {
	ReadManagedActualState(ctx context.Context) (ManagedRuntimeInventoryState, error)
}

// ManagedRuntimeInventoryState is the sanitized runtime data used to classify
// existing Gordon resources. It contains no environment, mount path, socket,
// or raw inspect fields.
type ManagedRuntimeInventoryState struct {
	Containers []ManagedRuntimeContainer
	Networks   []ManagedRuntimeNetwork
	Volumes    []ManagedRuntimeVolume
}

type ManagedRuntimeContainer struct {
	ID     string
	Name   string
	Status string
	Labels map[string]string
}

type ManagedRuntimeNetwork struct {
	Name   string
	Labels map[string]string
}

type ManagedRuntimeVolume struct {
	Name   string
	Labels map[string]string
}

// ManagedInventoryOptions supplies existing configured names and locations;
// it never creates paths or probes a secret value.
type ManagedInventoryOptions struct {
	NetworkPrefix           string
	VolumePrefix            string
	DataDir                 string
	EnvDir                  string
	RegistryStoragePath     string
	SecretsBackend          string
	SecretsBackendAvailable bool
}

// ManagedInventoryProvider obtains an inventory without mutation. Runtime
// implementations belong to gordon-runtime in split mode.
type ManagedInventoryProvider struct {
	actualState ManagedActualStateInventory
	options     ManagedInventoryOptions
}

func NewManagedInventoryProvider(actualState ManagedActualStateInventory, options ManagedInventoryOptions) *ManagedInventoryProvider {
	if strings.TrimSpace(options.NetworkPrefix) == "" {
		options.NetworkPrefix = "gordon"
	}
	if strings.TrimSpace(options.VolumePrefix) == "" {
		options.VolumePrefix = "gordon"
	}
	if options.RegistryStoragePath == "" && options.DataDir != "" {
		options.RegistryStoragePath = filepath.Join(options.DataDir, "registry")
	}
	return &ManagedInventoryProvider{actualState: actualState, options: options}
}

// ManagedInventory is the migration-safe view of existing Gordon resources.
// Names and declared public Gordon labels are retained; unknown labels are
// intentionally excluded so an adapter cannot leak inspect metadata.
type ManagedInventory struct {
	RouteContainers             []ManagedInventoryObject
	AttachmentContainers        []ManagedInventoryObject
	StandaloneServiceContainers []ManagedInventoryObject
	ComponentContainers         []ManagedInventoryObject
	StaleNextContainers         []ManagedInventoryObject
	ManagedNetworks             []ManagedInventoryObject
	ManagedVolumes              []ManagedInventoryObject
	DataDir                     InventoryPathAvailability
	EnvDir                      InventoryPathAvailability
	RegistryStorage             InventoryPathAvailability
	SecretsBackend              SecretBackendAvailability
	RuntimeAuthorityAccessPaths []RuntimeAuthorityAccessPath
}

type ManagedInventoryObject struct {
	Name   string
	Status string
	Labels map[string]string
}

// Names returns resource names in their deterministic inventory order.
func (ManagedInventory) Names(objects []ManagedInventoryObject) []string {
	names := make([]string, 0, len(objects))
	for _, object := range objects {
		names = append(names, object.Name)
	}
	return names
}

type InventoryPathAvailability struct {
	Path      string
	Available bool
	Reason    string
}

type SecretBackendAvailability struct {
	Backend   string
	Available bool
}

// RuntimeAuthorityAccessPath records known production paths found during the
// split-boundary audit. It is metadata only, not a socket probe.
type RuntimeAuthorityAccessPath struct {
	Package              string
	Abstraction          string
	SplitRole            string
	RuntimeAuthority     bool
	RequiresSplitRemoval bool
}

// Inventory returns only a snapshot. It does not invoke inspect, create,
// delete, connect, or any secret provider operation.
func (p *ManagedInventoryProvider) Inventory(ctx context.Context) (ManagedInventory, error) {
	if p == nil || p.actualState == nil {
		return ManagedInventory{}, fmt.Errorf("managed actual-state inventory is not configured")
	}
	state, err := p.actualState.ReadManagedActualState(ctx)
	if err != nil {
		return ManagedInventory{}, fmt.Errorf("read managed actual state: %w", err)
	}

	inventory := ManagedInventory{
		DataDir:                     inventoryPathAvailability(p.options.DataDir),
		EnvDir:                      inventoryPathAvailability(p.options.EnvDir),
		RegistryStorage:             inventoryPathAvailability(p.options.RegistryStoragePath),
		SecretsBackend:              SecretBackendAvailability{Backend: p.options.SecretsBackend, Available: p.options.SecretsBackendAvailable},
		RuntimeAuthorityAccessPaths: defaultRuntimeAuthorityAccessPaths(),
	}
	for _, container := range state.Containers {
		object := inventoryObject(container.Name, container.Status, container.Labels)
		switch {
		case isManagedStaleContainer(container):
			inventory.StaleNextContainers = append(inventory.StaleNextContainers, object)
		case isGordonComponentContainer(container):
			inventory.ComponentContainers = append(inventory.ComponentContainers, object)
		case isManagedAttachmentContainer(container):
			inventory.AttachmentContainers = append(inventory.AttachmentContainers, object)
		case isManagedStandaloneServiceContainer(container):
			inventory.StandaloneServiceContainers = append(inventory.StandaloneServiceContainers, object)
		case isManagedRouteContainer(container):
			inventory.RouteContainers = append(inventory.RouteContainers, object)
		}
	}
	for _, network := range state.Networks {
		if isManagedNetwork(network, p.options.NetworkPrefix) {
			inventory.ManagedNetworks = append(inventory.ManagedNetworks, inventoryObject(network.Name, "", network.Labels))
		}
	}
	for _, volume := range state.Volumes {
		if isManagedVolume(volume, p.options.VolumePrefix) {
			inventory.ManagedVolumes = append(inventory.ManagedVolumes, inventoryObject(volume.Name, "", volume.Labels))
		}
	}
	inventory.sort()
	return inventory, nil
}

func inventoryPathAvailability(path string) InventoryPathAvailability {
	path = strings.TrimSpace(path)
	if path == "" {
		return InventoryPathAvailability{Reason: "not configured"}
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return InventoryPathAvailability{Path: path, Reason: "missing"}
		}
		return InventoryPathAvailability{Path: path, Reason: "unavailable"}
	}
	if !info.IsDir() {
		return InventoryPathAvailability{Path: path, Reason: "not a directory"}
	}
	// Opening a directory verifies read access without creating or changing it.
	dir, err := os.Open(path)
	if err != nil {
		return InventoryPathAvailability{Path: path, Reason: "unreadable"}
	}
	if err := dir.Close(); err != nil {
		return InventoryPathAvailability{Path: path, Reason: "unavailable"}
	}
	return InventoryPathAvailability{Path: path, Available: true}
}

func inventoryObject(name, status string, labels map[string]string) ManagedInventoryObject {
	return ManagedInventoryObject{Name: name, Status: status, Labels: domain.SanitizeRuntimeStateLabels(labels)}
}

func isManagedRouteContainer(container ManagedRuntimeContainer) bool {
	return container.Labels[domain.LabelManaged] == "true" &&
		container.Labels[domain.LabelAttachment] != "true" &&
		container.Labels[domain.LabelService] != "true" &&
		(container.Labels[domain.LabelRoute] != "" || container.Labels[domain.LabelDomain] != "")
}

func isManagedAttachmentContainer(container ManagedRuntimeContainer) bool {
	return container.Labels[domain.LabelManaged] == "true" && container.Labels[domain.LabelAttachment] == "true"
}

func isManagedStandaloneServiceContainer(container ManagedRuntimeContainer) bool {
	return container.Labels[domain.LabelService] == "true"
}

func isManagedStaleContainer(container ManagedRuntimeContainer) bool {
	if container.Labels[domain.LabelManaged] != "true" {
		return false
	}
	return strings.HasSuffix(container.Name, "-new") || strings.HasSuffix(container.Name, "-next")
}

func isGordonComponentContainer(container ManagedRuntimeContainer) bool {
	switch container.Name {
	case "gordon-control", "gordon-runtime", "gordon-edge", "gordon-registry":
		return true
	default:
		return false
	}
}

func isManagedNetwork(network ManagedRuntimeNetwork, prefix string) bool {
	return network.Labels[domain.LabelManaged] == "true" && strings.HasPrefix(network.Name, prefix+"-")
}

func isManagedVolume(volume ManagedRuntimeVolume, prefix string) bool {
	return strings.HasPrefix(volume.Name, prefix+"-") || strings.HasPrefix(volume.Name, "gordon-service-")
}

func (i *ManagedInventory) sort() {
	for _, objects := range [][]ManagedInventoryObject{
		i.RouteContainers,
		i.AttachmentContainers,
		i.StandaloneServiceContainers,
		i.ComponentContainers,
		i.StaleNextContainers,
		i.ManagedNetworks,
		i.ManagedVolumes,
	} {
		sort.Slice(objects, func(left, right int) bool { return objects[left].Name < objects[right].Name })
	}
}

func defaultRuntimeAuthorityAccessPaths() []RuntimeAuthorityAccessPath {
	return []RuntimeAuthorityAccessPath{
		{Package: "internal/adapters/out/docker/runtime.go", Abstraction: "pkg/runtime.Runtime adapter", SplitRole: "runtime", RuntimeAuthority: true},
		{Package: "internal/usecase/container/service.go", Abstraction: "out.ContainerRuntime", SplitRole: "runtime", RuntimeAuthority: true},
		{Package: "internal/usecase/images/service.go", Abstraction: "pkg/runtime imageRuntime", SplitRole: "control", RequiresSplitRemoval: true},
		{Package: "internal/usecase/images/runtime_image_manager.go", Abstraction: "local RuntimeImageManager", SplitRole: "control", RequiresSplitRemoval: true},
		{Package: "internal/usecase/logs/runtime_log_reader.go", Abstraction: "local RuntimeLogReader", SplitRole: "control", RequiresSplitRemoval: true},
		{Package: "internal/usecase/volumes/runtime_volume_manager.go", Abstraction: "local RuntimeVolumeManager", SplitRole: "control", RequiresSplitRemoval: true},
		{Package: "internal/app/run.go", Abstraction: "monolith docker runtime wiring", SplitRole: "monolith", RuntimeAuthority: true, RequiresSplitRemoval: true},
	}
}
