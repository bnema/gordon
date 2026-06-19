package backup

import (
	"sort"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// SelectVolumeBackupTargets returns deterministic volume backup targets from Gordon-managed containers.
func SelectVolumeBackupTargets(containers []*domain.Container, volumePrefix string) []domain.VolumeBackupTarget {
	return SelectVolumeBackupTargetsForScope(containers, volumePrefix, "", "")
}

// SelectVolumeBackupTargetsForScope returns deterministic volume backup targets after applying domain/volume filters.
func SelectVolumeBackupTargetsForScope(containers []*domain.Container, volumePrefix, domainFilter, volumeFilter string) []domain.VolumeBackupTarget {
	targets := make([]domain.VolumeBackupTarget, 0)
	seenVolumes := make(map[string]struct{})

	for _, c := range sortedVolumeBackupContainers(containers) {
		domainName, ok := selectedVolumeBackupContainerDomain(c, domainFilter)
		if !ok {
			continue
		}
		for _, mount := range selectedVolumeBackupMounts(c.VolumeMounts, volumePrefix, volumeFilter) {
			if _, ok := seenVolumes[mount.Name]; ok {
				continue
			}
			seenVolumes[mount.Name] = struct{}{}
			targets = append(targets, domain.VolumeBackupTarget{
				Domain:        domainName,
				ContainerName: c.Name,
				ContainerID:   c.ID,
				VolumeName:    mount.Name,
				MountPath:     mount.Destination,
			})
		}
	}

	return targets
}

func sortedVolumeBackupContainers(containers []*domain.Container) []*domain.Container {
	sorted := make([]*domain.Container, 0, len(containers))
	for _, c := range containers {
		if c != nil {
			sorted = append(sorted, c)
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		leftDomain := volumeBackupDomain(sorted[i])
		rightDomain := volumeBackupDomain(sorted[j])
		if leftDomain != rightDomain {
			return leftDomain < rightDomain
		}
		return sorted[i].Name < sorted[j].Name
	})
	return sorted
}

func selectedVolumeBackupContainerDomain(c *domain.Container, domainFilter string) (string, bool) {
	if c.Labels == nil || c.Labels[domain.LabelManaged] != "true" {
		return "", false
	}
	domainName := volumeBackupDomain(c)
	if domainName == "" || (domainFilter != "" && domainName != domainFilter) {
		return "", false
	}
	return domainName, true
}

func selectedVolumeBackupMounts(mounts []domain.ContainerVolumeMount, volumePrefix, volumeFilter string) []domain.ContainerVolumeMount {
	selected := append([]domain.ContainerVolumeMount(nil), mounts...)
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Name != selected[j].Name {
			return selected[i].Name < selected[j].Name
		}
		return selected[i].Destination < selected[j].Destination
	})
	out := selected[:0]
	for _, mount := range selected {
		if volumeFilter != "" && mount.Name != volumeFilter {
			continue
		}
		if !isEligibleVolumeMount(mount, volumePrefix) {
			continue
		}
		out = append(out, mount)
	}
	return out
}

func volumeBackupDomain(c *domain.Container) string {
	if c == nil || c.Labels == nil {
		return ""
	}
	if c.Labels[domain.LabelAttachment] == "true" {
		return c.Labels[domain.LabelAttachedTo]
	}
	if domainName := c.Labels[domain.LabelDomain]; domainName != "" {
		return domainName
	}
	return c.Labels[domain.LabelRoute]
}

func isEligibleVolumeMount(mount domain.ContainerVolumeMount, volumePrefix string) bool {
	if mount.Type != "volume" || mount.Name == "" || mount.Destination == "" {
		return false
	}
	prefix := strings.TrimSpace(volumePrefix)
	if prefix == "" {
		return true
	}
	return strings.HasPrefix(mount.Name, prefix+"-")
}
