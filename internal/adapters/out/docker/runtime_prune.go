package docker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

var _ out.PruneRuntime = (*Runtime)(nil)

// InventoryRuntime implements out.PruneRuntime.
//
// It reads containers (running and stopped), images, and volumes in one
// pass. A failed list is recorded as an inventory gap, never as
// absence: the caller then fails closed per candidate instead of
// planning against an empty runtime.
func (r *Runtime) InventoryRuntime(ctx context.Context) (*domain.RuntimeInventory, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "docker",
		zerowrap.FieldAction:  "InventoryRuntime",
	})
	log := zerowrap.FromCtx(ctx)

	inventory := &domain.RuntimeInventory{}

	containerList, containerErr := r.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if containerErr != nil {
		log.Warn().Err(containerErr).Msg("failed to list containers for prune inventory")
		inventory.Gaps = append(inventory.Gaps, domain.InventoryGap{
			Source: domain.InventorySourceRuntimeContainers,
			Reason: domain.PruneReasonUnknownContainerUse,
			Detail: "list containers failed",
		})
	} else {
		inventory.Containers = containerImageUses(containerList.Items)
	}

	imageList, imageErr := r.client.ImageList(ctx, client.ImageListOptions{All: true})
	if imageErr != nil {
		log.Warn().Err(imageErr).Msg("failed to list images for prune inventory")
		inventory.Gaps = append(inventory.Gaps, domain.InventoryGap{
			Source: domain.InventorySourceRuntimeImages,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: "list images failed",
		})
	} else {
		inventory.Images = runtimeImages(imageList.Items)
	}

	volumeList, volumeErr := r.client.VolumeList(ctx, client.VolumeListOptions{})
	if volumeErr != nil {
		log.Warn().Err(volumeErr).Msg("failed to list volumes for prune inventory")
		inventory.Gaps = append(inventory.Gaps, domain.InventoryGap{
			Source: domain.InventorySourceRuntimeVolumes,
			Reason: domain.PruneReasonUnknownInventory,
			Detail: "list volumes failed",
		})
	} else {
		var mounts []volumeMount
		if containerErr == nil {
			mounts = volumeMounts(containerList.Items)
		}
		inventory.Volumes = volumeInfos(volumeList.Items, mounts)
	}

	return inventory, nil
}

// volumeMount is one volume mount observed on a container.
type volumeMount struct {
	volume    string
	container string
}

func volumeMounts(containers []container.Summary) []volumeMount {
	var mounts []volumeMount
	for _, container := range containers {
		name := ""
		if len(container.Names) > 0 {
			name = strings.TrimPrefix(container.Names[0], "/")
		}
		if name == "" {
			name = container.ID
		}
		for _, mount := range container.Mounts {
			if mount.Type != "volume" || mount.Name == "" {
				continue
			}
			mounts = append(mounts, volumeMount{volume: mount.Name, container: name})
		}
	}
	sort.Slice(mounts, func(i, j int) bool {
		if mounts[i].volume != mounts[j].volume {
			return mounts[i].volume < mounts[j].volume
		}
		return mounts[i].container < mounts[j].container
	})
	return mounts
}

// containerImageUses maps every container, running or not, to the image
// identity it was created from. A container whose image identity cannot
// be resolved marks ImageUnknown so no candidate can be proven unused.
func containerImageUses(containers []container.Summary) []domain.RuntimeContainerUse {
	uses := make([]domain.RuntimeContainerUse, 0, len(containers))
	for _, container := range containers {
		use := domain.RuntimeContainerUse{
			ContainerID: container.ID,
			ImageID:     container.ImageID,
			ImageRef:    container.Image,
			Running:     container.State == "running",
		}
		if use.ImageID == "" && use.ImageRef == "" {
			use.ImageUnknown = true
		}
		uses = append(uses, use)
	}
	sort.Slice(uses, func(i, j int) bool { return uses[i].ContainerID < uses[j].ContainerID })
	return uses
}

// runtimeImages maps the runtime image list to inventory entries.
func runtimeImages(images []image.Summary) []domain.RuntimeImage {
	out := make([]domain.RuntimeImage, 0, len(images))
	for _, image := range images {
		out = append(out, domain.RuntimeImage{
			ID:          image.ID,
			RepoTags:    append([]string(nil), image.RepoTags...),
			RepoDigests: append([]string(nil), image.RepoDigests...),
			Labels:      image.Labels,
			Size:        image.Size,
			Created:     time.Unix(image.Created, 0),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// volumeInfos maps volumes plus observed container mounts to inventory
// entries.
func volumeInfos(volumes []volume.Volume, mounts []volumeMount) []*domain.VolumeInfo {
	users := make(map[string][]string)
	for _, mount := range mounts {
		users[mount.volume] = append(users[mount.volume], mount.container)
	}

	out := make([]*domain.VolumeInfo, 0, len(volumes))
	for _, volume := range volumes {
		var size int64
		if volume.UsageData != nil {
			size = volume.UsageData.Size
		}
		// A volume driver that cannot report creation time yields the
		// zero time (for example some Podman responses). That is not a
		// safety input: eligibility comes from ownership, not age.
		created, _ := time.Parse(time.RFC3339, volume.CreatedAt)
		containers := users[volume.Name]
		out = append(out, &domain.VolumeInfo{
			Name:       volume.Name,
			Driver:     volume.Driver,
			MountPoint: volume.Mountpoint,
			Size:       size,
			CreatedAt:  created,
			InUse:      len(containers) > 0,
			Containers: containers,
			Labels:     volume.Labels,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RemoveImageExact implements out.PruneRuntime. It removes one exact
// runtime image ID and never forces: an image that is still referenced
// fails loudly instead of being untagged behind the caller's back.
func (r *Runtime) RemoveImageExact(ctx context.Context, ref domain.RuntimeImageRef) error {
	if !ref.Valid() {
		return fmt.Errorf("docker: refusing to remove image with invalid identity %q", ref.ID)
	}
	return r.RemoveImage(ctx, ref.ID, false)
}

// RemoveVolumeExact implements out.PruneRuntime. It removes one exact
// runtime volume name and never forces.
func (r *Runtime) RemoveVolumeExact(ctx context.Context, ref domain.RuntimeVolumeRef) error {
	if !ref.Valid() {
		return fmt.Errorf("docker: refusing to remove volume with invalid identity %q", ref.Name)
	}
	return r.RemoveVolume(ctx, ref.Name, false)
}
