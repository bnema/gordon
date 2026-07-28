package container

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestComponentMountPlanAttestationParity(t *testing.T) {
	data := t.TempDir()
	migrationRoot := filepath.Join(data, "migration")
	configDir := filepath.Join(migrationRoot, "config", "fixture", "1")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	configPath := filepath.Join(configDir, "control.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[control]\n"), 0o600))

	manager := &runtimeComponentLifecycleManager{
		policy: RuntimePolicy{
			Mode:                        RuntimePolicyModeEnforce,
			MigrationStateRoot:          migrationRoot,
			ManagedControlSecretsVolume: "gordon-control-secrets-0123456789abcdef",
		},
	}
	profile, ok := domain.FixedRuntimeComponentLifecycleProfile(domain.ComponentRoleControl)
	require.True(t, ok)
	command := domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{
			ID:                "mount-plan-parity",
			IdempotencyKey:    "mount-plan-parity",
			Generation:        1,
			SourceComponentID: "gordon-control",
		},
		TargetComponentID:   "gordon-control-fixture-g1",
		TargetComponentRole: domain.ComponentRoleControl,
		PolicyDecisionID:    "migration:fixture",
		LifecycleProfile:    profile,
		LifecycleAction:     domain.RuntimeComponentLifecycleStart,
		ConfigFile:          configPath,
		DesiredImage:        "example.invalid/gordon:v2",
		DesiredStateHash:    "hash",
		InternalNetwork:     "gordon-internal-fixture-g1",
	}
	ports := []domain.ContainerPortPublish{{HostIP: "127.0.0.1", HostPort: 18080, ContainerPort: 8080, Protocol: domain.NetworkProtocolTCP}}

	config, err := manager.componentConfig(command, ports)
	require.NoError(t, err)

	createPlan, err := manager.componentMountPlanForCreate(command, ports)
	require.NoError(t, err)
	expected := createPlan.expectedMounts()

	attestPlan, err := manager.componentMountPlanForAttestation(command, ports, configPath, "")
	require.NoError(t, err)
	assert.Equal(t, expected, attestPlan.expectedMounts(), "create and attestation mount plans must match")

	for destination, mount := range expected {
		if mount.readOnly {
			assert.Equal(t, mount.source, config.ReadOnlyVolumes[destination])
			continue
		}
		assert.Equal(t, mount.source, config.Volumes[destination])
		if len(mount.options) > 0 {
			assert.Equal(t, mount.options, config.VolumeOptions[destination])
		}
	}
	assert.True(t, lifecycleMountsMatch(volumeMountsFromConfig(config), expected))
}

func volumeMountsFromConfig(config *domain.ContainerConfig) []domain.ContainerVolumeMount {
	mounts := make([]domain.ContainerVolumeMount, 0, len(config.Volumes)+len(config.ReadOnlyVolumes))
	for destination, source := range config.Volumes {
		mount := domain.ContainerVolumeMount{Type: "volume", Name: source, Destination: destination, Source: source}
		if opts, ok := config.VolumeOptions[destination]; ok {
			mount.Options = append([]string(nil), opts...)
		}
		if filepath.IsAbs(source) {
			mount.Type = "bind"
		}
		mounts = append(mounts, mount)
	}
	for destination, source := range config.ReadOnlyVolumes {
		mount := domain.ContainerVolumeMount{Type: "bind", Source: source, Destination: destination, ReadOnly: true}
		if filepath.IsAbs(source) == false {
			mount.Type = "volume"
			mount.Name = source
		}
		mounts = append(mounts, mount)
	}
	return mounts
}
