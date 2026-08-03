package volumes

import (
	"context"
	"fmt"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

type localRuntimeVolumeManager struct {
	runtime out.ContainerRuntime
}

// NewLocalRuntimeVolumeManager creates a monolith/local implementation of the narrow runtime volume port.
func NewLocalRuntimeVolumeManager(runtime out.ContainerRuntime) out.RuntimeVolumeManager {
	return localRuntimeVolumeManager{runtime: runtime}
}

func (m localRuntimeVolumeManager) ListRuntimeVolumes(ctx context.Context) ([]*domain.VolumeInfo, error) {
	if m.runtime == nil {
		return nil, fmt.Errorf("runtime volume manager not configured")
	}
	return m.runtime.ListVolumes(ctx)
}

func (m localRuntimeVolumeManager) RemoveRuntimeVolume(ctx context.Context, volumeName string, force bool) error {
	if m.runtime == nil {
		return fmt.Errorf("runtime volume manager not configured")
	}
	return m.runtime.RemoveVolume(ctx, volumeName, force)
}
