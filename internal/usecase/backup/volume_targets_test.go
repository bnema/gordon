package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestSelectVolumeBackupTargets(t *testing.T) {
	containers := []*domain.Container{
		{
			ID:   "unmanaged",
			Name: "unmanaged",
			Labels: map[string]string{
				domain.LabelDomain: "app.example.com",
			},
			VolumeMounts: []domain.ContainerVolumeMount{{Name: "gordon-app-data", Type: "volume", Destination: "/data"}},
		},
		{
			ID:   "app",
			Name: "app",
			Labels: map[string]string{
				domain.LabelManaged: "true",
				domain.LabelDomain:  "app.example.com",
			},
			VolumeMounts: []domain.ContainerVolumeMount{
				{Name: "gordon-app-example-com-data", Type: "volume", Destination: "/data"},
				{Name: "gordon-app-example-com-cache", Type: "bind", Destination: "/cache"},
				{Name: "random-anonymous", Type: "volume", Destination: "/anon"},
				{Name: "", Type: "volume", Destination: "/empty"},
			},
		},
		{
			ID:   "postgres",
			Name: "postgres",
			Labels: map[string]string{
				domain.LabelManaged:    "true",
				domain.LabelAttachment: "true",
				domain.LabelAttachedTo: "app.example.com",
			},
			VolumeMounts: []domain.ContainerVolumeMount{
				{Name: "gordon-postgres-data", Type: "volume", Destination: "/var/lib/postgresql/data"},
				{Name: "gordon-app-example-com-data", Type: "volume", Destination: "/shared"},
			},
		},
	}

	targets := SelectVolumeBackupTargets(containers, "gordon")

	assert.Equal(t, []domain.VolumeBackupTarget{
		{
			Domain:        "app.example.com",
			ContainerName: "app",
			ContainerID:   "app",
			VolumeName:    "gordon-app-example-com-data",
			MountPath:     "/data",
		},
		{
			Domain:        "app.example.com",
			ContainerName: "postgres",
			ContainerID:   "postgres",
			VolumeName:    "gordon-postgres-data",
			MountPath:     "/var/lib/postgresql/data",
		},
	}, targets)
}

func TestSelectVolumeBackupTargetsForScopeDedupesAfterFiltering(t *testing.T) {
	containers := []*domain.Container{
		{
			ID:   "alpha",
			Name: "alpha",
			Labels: map[string]string{
				domain.LabelManaged: "true",
				domain.LabelDomain:  "alpha.example.com",
			},
			VolumeMounts: []domain.ContainerVolumeMount{{Name: "gordon-shared", Type: "volume", Destination: "/data"}},
		},
		{
			ID:   "beta",
			Name: "beta",
			Labels: map[string]string{
				domain.LabelManaged: "true",
				domain.LabelDomain:  "beta.example.com",
			},
			VolumeMounts: []domain.ContainerVolumeMount{{Name: "gordon-shared", Type: "volume", Destination: "/data"}},
		},
	}

	targets := SelectVolumeBackupTargetsForScope(containers, "gordon", "beta.example.com", "")

	require.Len(t, targets, 1)
	assert.Equal(t, "beta.example.com", targets[0].Domain)
	assert.Equal(t, "gordon-shared", targets[0].VolumeName)
}

func TestSelectVolumeBackupTargetsRequiresDomain(t *testing.T) {
	containers := []*domain.Container{
		{
			ID:   "missing-domain",
			Name: "missing-domain",
			Labels: map[string]string{
				domain.LabelManaged: "true",
			},
			VolumeMounts: []domain.ContainerVolumeMount{{Name: "gordon-data", Type: "volume", Destination: "/data"}},
		},
	}

	targets := SelectVolumeBackupTargets(containers, "gordon")

	assert.Empty(t, targets)
}
