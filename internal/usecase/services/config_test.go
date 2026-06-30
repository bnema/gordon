package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestToDomainRejectsDuplicateServiceNames(t *testing.T) {
	configs := []Config{
		{Name: "cache", Image: "redis:7", Enabled: true},
		{Name: "cache", Image: "redis:7", Enabled: true},
	}

	_, err := ToDomain(configs)

	require.ErrorContains(t, err, "duplicate service name")
}

func TestConfigToDomainRejectsNonPositiveReadinessTimeout(t *testing.T) {
	for _, timeout := range []string{"0s", "-1s"} {
		t.Run(timeout, func(t *testing.T) {
			cfg := Config{
				Name:    "cache",
				Image:   "redis:7",
				Enabled: true,
				Readiness: ReadinessConfig{
					Type:    domain.StandaloneServiceReadinessTCP,
					Timeout: timeout,
				},
			}

			_, err := cfg.ToDomain()

			require.ErrorContains(t, err, "readiness timeout")
			require.ErrorContains(t, err, "positive")
		})
	}
}

func TestResolveVolumeMountsUsesExplicitVolumesAsIs(t *testing.T) {
	explicit := []domain.StandaloneServiceVolume{
		{Source: "rust-data", Target: "/steamcmd/rust", ReadOnly: false},
		{Source: "/srv/config", Target: "/config", ReadOnly: true},
	}

	mounts := ResolveVolumeMounts("gordon", "rust.example.com", explicit, []string{"/ignored"})

	assert.Equal(t, []ResolvedVolumeMount{
		{Source: "rust-data", Target: "/steamcmd/rust", ReadOnly: false},
		{Source: "/srv/config", Target: "/config", ReadOnly: true},
	}, mounts)
}

func TestResolveVolumeMountsUsesImageDiscoveredVolumesWhenExplicitOmitted(t *testing.T) {
	mounts := ResolveVolumeMounts("gordon", "rust.example.com", nil, []string{"/steamcmd/rust", "/var/lib/data"})

	assert.Equal(t, []ResolvedVolumeMount{
		{Source: "gordon-rust-example-com-steamcmd-rust", Target: "/steamcmd/rust"},
		{Source: "gordon-rust-example-com-var-lib-data", Target: "/var/lib/data"},
	}, mounts)
}

func TestResolveVolumeMountsReturnsEmptyForStatelessImage(t *testing.T) {
	mounts := ResolveVolumeMounts("gordon", "stateless.example.com", nil, nil)

	assert.Empty(t, mounts)
}

func TestManagedServiceVolumeNameIsDeterministicAndGordonCompatible(t *testing.T) {
	assert.Equal(t, "gordon-rust-example-com-var-lib-data", ManagedServiceVolumeName("gordon", "rust.example.com", "/var/lib/data"))
	assert.Equal(t, "gordon-rust-example-com-data", ManagedServiceVolumeName("gordon", "rust.example.com", "/data"))
}
