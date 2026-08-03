package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

type managedInventoryReaderFake struct {
	state ManagedRuntimeInventoryState
	err   error
	calls int
}

func (f *managedInventoryReaderFake) ReadManagedActualState(context.Context) (ManagedRuntimeInventoryState, error) {
	f.calls++
	return f.state, f.err
}

func TestManagedInventoryClassification(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	envDir := filepath.Join(dataDir, "env")
	registryDir := filepath.Join(dataDir, "registry")
	require.NoError(t, mkdirAll(dataDir, envDir, registryDir))

	reader := &managedInventoryReaderFake{state: ManagedRuntimeInventoryState{
		Containers: []ManagedRuntimeContainer{
			{Name: "gordon-app.example.test", Status: "running", Labels: map[string]string{domain.LabelManaged: "true", domain.LabelRoute: "app.example.test", "untrusted.inspect": "must-not-leak"}},
			{Name: "gordon-app.example.test-db", Status: "running", Labels: map[string]string{domain.LabelManaged: "true", domain.LabelAttachment: "true", domain.LabelAttachedTo: "app.example.test"}},
			{Name: "gordon-metrics", Status: "running", Labels: map[string]string{domain.LabelService: "true", domain.LabelServiceName: "metrics"}},
			{Name: "gordon-app.example.test-next", Status: "created", Labels: map[string]string{domain.LabelManaged: "true", domain.LabelRoute: "app.example.test"}},
			{Name: "gordon-edge", Status: "running", Labels: map[string]string{domain.LabelManaged: "true"}},
			{Name: "unrelated", Status: "running", Labels: map[string]string{domain.LabelManaged: "true"}},
		},
		Networks: []ManagedRuntimeNetwork{
			{Name: "gordon-app-example-test", Labels: map[string]string{domain.LabelManaged: "true", "untrusted.inspect": "must-not-leak"}},
			{Name: "other", Labels: map[string]string{domain.LabelManaged: "true"}},
		},
		Volumes: []ManagedRuntimeVolume{
			{Name: "gordon-app-example-test-data", Labels: map[string]string{domain.LabelManaged: "true", "untrusted.inspect": "must-not-leak"}},
			{Name: "other-data", Labels: map[string]string{domain.LabelManaged: "true"}},
		},
	}}

	inventory, err := NewManagedInventoryProvider(reader, ManagedInventoryOptions{
		NetworkPrefix:           "gordon",
		VolumePrefix:            "gordon",
		DataDir:                 dataDir,
		EnvDir:                  envDir,
		RegistryStoragePath:     registryDir,
		SecretsBackend:          "pass",
		SecretsBackendAvailable: true,
	}).Inventory(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{"gordon-app.example.test"}, inventory.Names(inventory.RouteContainers))
	assert.Equal(t, []string{"gordon-app.example.test-db"}, inventory.Names(inventory.AttachmentContainers))
	assert.Equal(t, []string{"gordon-metrics"}, inventory.Names(inventory.StandaloneServiceContainers))
	assert.Equal(t, []string{"gordon-edge"}, inventory.Names(inventory.ComponentContainers))
	assert.Equal(t, []string{"gordon-app.example.test-next"}, inventory.Names(inventory.StaleNextContainers))
	assert.Equal(t, []string{"gordon-app-example-test"}, inventory.Names(inventory.ManagedNetworks))
	assert.Equal(t, []string{"gordon-app-example-test-data"}, inventory.Names(inventory.ManagedVolumes))
	assert.Equal(t, "true", inventory.RouteContainers[0].Labels[domain.LabelManaged])
	assert.Equal(t, "app.example.test", inventory.RouteContainers[0].Labels[domain.LabelRoute])
	assert.NotContains(t, inventory.RouteContainers[0].Labels, "untrusted.inspect")
	assert.NotContains(t, inventory.ManagedNetworks[0].Labels, "untrusted.inspect")
	assert.True(t, inventory.DataDir.Available)
	assert.True(t, inventory.RegistryStorage.Available)
	assert.True(t, inventory.EnvDir.Available)
	assert.True(t, inventory.SecretsBackend.Available)
	assert.Equal(t, 1, reader.calls)
	assert.NotEmpty(t, inventory.RuntimeAuthorityAccessPaths)
	assert.Contains(t, inventory.RuntimeAuthorityAccessPaths, RuntimeAuthorityAccessPath{Package: "internal/usecase/images/service.go", Abstraction: "pkg/runtime imageRuntime", SplitRole: "control", RequiresSplitRemoval: true})
	assert.NotContains(t, inventory.RuntimeAuthorityAccessPaths, RuntimeAuthorityAccessPath{Package: "raw inspect"})
}

func TestManagedInventoryAvailabilityAndReadOnlyFailure(t *testing.T) {
	reader := &managedInventoryReaderFake{err: assert.AnError}
	_, err := NewManagedInventoryProvider(reader, ManagedInventoryOptions{}).Inventory(context.Background())
	require.ErrorIs(t, err, assert.AnError)
	require.Equal(t, 1, reader.calls)

	inventory, err := NewManagedInventoryProvider(&managedInventoryReaderFake{}, ManagedInventoryOptions{
		DataDir:        filepath.Join(t.TempDir(), "missing-data"),
		EnvDir:         filepath.Join(t.TempDir(), "missing-env"),
		SecretsBackend: "pass",
	}).Inventory(context.Background())
	require.NoError(t, err)
	assert.False(t, inventory.DataDir.Available)
	assert.False(t, inventory.EnvDir.Available)
	assert.False(t, inventory.SecretsBackend.Available)
}

func mkdirAll(paths ...string) error {
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o750); err != nil {
			return err
		}
	}
	return nil
}
