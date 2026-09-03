package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestToDomainUsesKeyedServiceAndPortNames(t *testing.T) {
	configs := map[string]Config{
		"app": {
			Image: "app:latest",
			Ports: map[string]PortConfig{
				"web":  {Container: 8080},
				"game": {Container: 9000, Protocol: domain.ServicePortProtocolUDP},
			},
		},
	}

	services, err := ToDomain(configs)

	require.NoError(t, err)
	require.Len(t, services, 1)
	assert.Equal(t, "app", services[0].Name)
	assert.Equal(t, []domain.StandaloneServicePort{
		{Name: "game", Container: 9000, Protocol: domain.ServicePortProtocolUDP},
		{Name: "web", Container: 8080, Protocol: domain.ServicePortProtocolTCP},
	}, services[0].Ports)
}

func TestToDomainRejectsServiceNamesWithDuplicateRuntimeIdentifier(t *testing.T) {
	configs := map[string]Config{
		"cache.api": {Image: "redis:7"},
		"cache_api": {Image: "redis:7"},
	}

	_, err := ToDomain(configs)

	require.ErrorContains(t, err, "runtime identifier")
}

func TestConfigToDomainRejectsNonPositiveReadinessTimeout(t *testing.T) {
	for _, timeout := range []string{"0s", "-1s"} {
		t.Run(timeout, func(t *testing.T) {
			cfg := Config{
				Image: "redis:7",
				Readiness: ReadinessConfig{
					Type:    domain.StandaloneServiceReadinessTCP,
					Timeout: timeout,
				},
			}

			_, err := cfg.ToDomain("cache")

			require.ErrorContains(t, err, "readiness timeout")
			require.ErrorContains(t, err, "positive")
		})
	}
}

func TestConfigToDomainRejectsExplicitNoCleanupAction(t *testing.T) {
	preserveVolumes := false
	removeContainer := false
	cfg := Config{
		Image:   "redis:7",
		Cleanup: CleanupConfig{PreserveVolumes: &preserveVolumes, RemoveContainer: &removeContainer},
	}

	_, err := cfg.ToDomain("cache")

	require.ErrorContains(t, err, "preserve_volumes=false")
	require.ErrorContains(t, err, "remove_container=false")
}

func TestConfigToDomainMapsExplicitPortPolicy(t *testing.T) {
	cfg := Config{
		Image: "game:latest",
		Ports: map[string]PortConfig{
			"rcon": {
				Container: 28016,
				Protocol:  domain.ServicePortProtocolTCP,
				Public:    true,
			},
		},
	}

	svc, err := cfg.ToDomain("game")

	require.NoError(t, err)
	require.Len(t, svc.Ports, 1)
	assert.True(t, svc.Ports[0].Public)
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
		{Source: "gordon-rust-example-com-steamcmd-rust", Target: "/steamcmd/rust", Managed: true},
		{Source: "gordon-rust-example-com-var-lib-data", Target: "/var/lib/data", Managed: true},
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
