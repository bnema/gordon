package preview

import (
	"context"
	"fmt"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// VolumeCloner abstracts container runtime operations needed for volume cloning.
type VolumeCloner interface {
	CreateVolume(ctx context.Context, name string) error
	RemoveVolume(ctx context.Context, name string, force bool) error
	CreateContainer(ctx context.Context, config *domain.ContainerConfig) (*domain.Container, error)
	StartContainer(ctx context.Context, containerID string) error
	WaitForContainer(ctx context.Context, containerID string) error
	RemoveContainer(ctx context.Context, containerID string, force bool) error
	ListVolumes(ctx context.Context) ([]*domain.VolumeInfo, error)
}

// VolumeNamer is a function that maps a source volume name to a destination volume name.
type VolumeNamer func(sourceVolName string) string

// DefaultNamer returns a VolumeNamer that uses BuildCloneVolumeName with the given preview name.
func DefaultNamer(previewName string) VolumeNamer {
	return func(sourceVolName string) string {
		return BuildCloneVolumeName(previewName, sourceVolName)
	}
}

// BuildCloneVolumeName generates a preview volume name.
func BuildCloneVolumeName(previewName, volumeName string) string {
	return "preview-" + previewName + "-" + volumeName
}

// BuildCloneContainerName generates a preview attachment container name from an image reference.
func BuildCloneContainerName(previewName, image string) string {
	name := image
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.Index(name, ":"); i >= 0 {
		name = name[:i]
	}
	return "preview-" + previewName + "-" + name
}

// CloneVolumes copies volumes from source using a read-only helper container.
// sourceVolumes is a list of actual Docker volume names to clone.
func CloneVolumes(ctx context.Context, cloner VolumeCloner, namer VolumeNamer, sourceVolumes []string) ([]string, error) {
	var clonedNames []string
	for _, sourceVolName := range sourceVolumes {
		destName := namer(sourceVolName)

		if err := cloner.CreateVolume(ctx, destName); err != nil {
			cleanupVolumes(ctx, cloner, clonedNames)
			return nil, fmt.Errorf("create volume %s: %w", destName, err)
		}

		helperConfig := &domain.ContainerConfig{
			Image: "busybox:1.37",
			Name:  "gordon-vol-copy-" + destName,
			Cmd:   []string{"cp", "-a", "/src/.", "/dst/"},
			Volumes: map[string]string{
				"/dst": destName,
			},
			ReadOnlyVolumes: map[string]string{
				"/src": sourceVolName,
			},
		}

		created, err := cloner.CreateContainer(ctx, helperConfig)
		if err != nil {
			_ = cloner.RemoveVolume(ctx, destName, true)
			cleanupVolumes(ctx, cloner, clonedNames)
			return nil, fmt.Errorf("create copy helper for %s: %w", destName, err)
		}

		if err := cloner.StartContainer(ctx, created.ID); err != nil {
			_ = cloner.RemoveContainer(ctx, created.ID, true)
			_ = cloner.RemoveVolume(ctx, destName, true)
			cleanupVolumes(ctx, cloner, clonedNames)
			return nil, fmt.Errorf("start copy helper for %s: %w", destName, err)
		}

		if err := cloner.WaitForContainer(ctx, created.ID); err != nil {
			_ = cloner.RemoveContainer(ctx, created.ID, true)
			_ = cloner.RemoveVolume(ctx, destName, true)
			cleanupVolumes(ctx, cloner, clonedNames)
			return nil, fmt.Errorf("wait for copy helper %s: %w", destName, err)
		}

		_ = cloner.RemoveContainer(ctx, created.ID, true)
		clonedNames = append(clonedNames, destName)
	}
	return clonedNames, nil
}

func cleanupVolumes(ctx context.Context, cloner VolumeCloner, names []string) {
	for _, name := range names {
		_ = cloner.RemoveVolume(ctx, name, true)
	}
}
