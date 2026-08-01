package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func testMigrationCheckpoint() MigrationCheckpoint {
	return MigrationCheckpoint{MigrationID: "migration-1", StartedAt: time.Now().UTC(), Phase: MigrationPhasePlanned, ComponentGeneration: 1, EnvFileReferences: []string{"/redacted/env"}}
}

func TestMigrationCheckpointLoadAndSaveRejectAlternateRuntimeEndpointSpellings(t *testing.T) {
	canonical := "unix:///var/lib/gordon/migration/migration-1/runtime-control.sock"
	checkpoint := testMigrationCheckpoint()
	checkpoint.BootstrapRuntimeEndpoint = canonical

	canonicalStore, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	require.NoError(t, canonicalStore.Save(checkpoint))
	loaded, err := canonicalStore.Load()
	require.NoError(t, err)
	assert.Equal(t, canonical, loaded.BootstrapRuntimeEndpoint)

	for _, endpoint := range alternateRuntimeBootstrapEndpoints("migration-1") {
		t.Run(endpoint.name, func(t *testing.T) {
			alternate := checkpoint
			alternate.BootstrapRuntimeEndpoint = endpoint.value

			writeStore, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
			require.NoError(t, err)
			assert.Error(t, writeStore.Save(alternate), "checkpoint writes must reject %q", endpoint.value)

			loadPath := filepath.Join(t.TempDir(), "checkpoint.json")
			encoded, err := json.Marshal(alternate)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(loadPath, encoded, 0o600))
			loadStore, err := NewMigrationCheckpointStore(loadPath)
			require.NoError(t, err)
			_, err = loadStore.Load()
			assert.Error(t, err, "checkpoint loads must reject %q", endpoint.value)
		})
	}
}

func TestMigrationCheckpointRejectsDeadBootstrapControlEndpointField(t *testing.T) {
	checkpoint := testMigrationCheckpoint()
	encoded, err := json.Marshal(checkpoint)
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, json.Unmarshal(encoded, &document))
	document["bootstrap_control_endpoint"] = "127.0.0.1:9000"
	encoded, err = json.Marshal(document)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "checkpoint.json")
	require.NoError(t, os.WriteFile(path, encoded, 0o600))
	store, err := NewMigrationCheckpointStore(path)
	require.NoError(t, err)
	_, err = store.Load()
	assert.Error(t, err, "removed checkpoint fields must be rejected rather than silently retained")
}

func TestMigrationCheckpointDeleteRemovesDurableStateAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "checkpoint.json")
	store, err := NewMigrationCheckpointStore(path)
	require.NoError(t, err)
	require.NoError(t, store.Save(testMigrationCheckpoint()))
	require.FileExists(t, path)

	require.NoError(t, store.Delete())
	require.NoFileExists(t, path)
	loaded, err := store.Load()
	assert.Nil(t, loaded)
	assert.ErrorIs(t, err, os.ErrNotExist)

	// Deleting an already-absent checkpoint is a durable no-op so a fresh
	// prepare can always start from a clean slate.
	require.NoError(t, store.Delete())
}

func TestMigrationCheckpointAtomicMonotonicAndSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "checkpoint.json")
	store, err := NewMigrationCheckpointStore(path)
	require.NoError(t, err)
	checkpoint := testMigrationCheckpoint()
	require.NoError(t, store.Save(checkpoint))
	loaded, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, checkpoint.MigrationID, loaded.MigrationID)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	checkpoint.Phase = MigrationPhasePrepared
	require.NoError(t, store.Save(checkpoint))
	checkpoint.Phase = MigrationPhasePlanned
	assert.Error(t, store.Save(checkpoint))
	assert.NoFileExists(t, filepath.Join(filepath.Dir(path), "data-volume"))
}

func TestMigrationCheckpointRuntimeCutoverCommitIsDurableAndIdempotent(t *testing.T) {
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	checkpoint := testMigrationCheckpoint()
	checkpoint.Phase = MigrationPhasePrepared
	checkpoint.TargetImage = "example.invalid/gordon:v2"
	checkpoint.OldServingPath = "old-monolith"
	checkpoint.RouteSnapshotGeneration = 7
	checkpoint.AppliedEdgeComponentID = "gordon-edge-migration-1-g1"
	checkpoint.PublicPortBindings = []MigrationPortBinding{{Role: "edge", HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 8080, Protocol: "tcp"}}
	require.NoError(t, store.Save(checkpoint))
	command := domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: domain.RuntimeCommandIdentity{Generation: checkpoint.ComponentGeneration}, LifecycleAction: domain.RuntimeComponentLifecycleActivate, TargetComponentRole: domain.ComponentRoleEdge, TargetComponentID: checkpoint.AppliedEdgeComponentID, PolicyDecisionID: "migration:" + checkpoint.MigrationID, OldServingComponentID: checkpoint.OldServingPath, FinalPortPublishes: []domain.ContainerPortPublish{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 8080, Protocol: domain.NetworkProtocolTCP}}}

	require.NoError(t, store.CommitMigrationCutover(context.Background(), command))
	require.NoError(t, store.CommitMigrationCutover(context.Background(), command), "a retry after caller termination must converge")
	loaded, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, MigrationPhaseSwitched, loaded.Phase)
	assert.Empty(t, loaded.LastRetryPhase)

	command.OldServingComponentID = "other"
	assert.Error(t, store.CommitMigrationCutover(context.Background(), command), "a different cutover cannot rewrite durable status")
}

func TestMigrationCheckpointBindsEdgeAppNetworksIntoCutoverIdentity(t *testing.T) {
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	checkpoint := testMigrationCheckpoint()
	checkpoint.Phase = MigrationPhasePrepared
	checkpoint.TargetImage = "example.invalid/gordon:v2"
	checkpoint.OldServingPath = "old-monolith"
	checkpoint.RouteSnapshotGeneration = 7
	checkpoint.AppliedEdgeComponentID = "gordon-edge-migration-1-g1"
	checkpoint.EdgeAppNetworks = []string{"gordon-app-fixture", "gordon-app-other"}
	checkpoint.PublicPortBindings = []MigrationPortBinding{{Role: "edge", HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 8080, Protocol: "tcp"}}
	require.NoError(t, store.Save(checkpoint))
	command := domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{Generation: checkpoint.ComponentGeneration},
		LifecycleAction:        domain.RuntimeComponentLifecycleActivate,
		TargetComponentRole:    domain.ComponentRoleEdge,
		TargetComponentID:      checkpoint.AppliedEdgeComponentID,
		PolicyDecisionID:       "migration:" + checkpoint.MigrationID,
		OldServingComponentID:  checkpoint.OldServingPath,
		FinalPortPublishes:     []domain.ContainerPortPublish{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 8080, Protocol: domain.NetworkProtocolTCP}},
		EdgeAppNetworks:        []string{"gordon-app-fixture", "gordon-app-other"},
	}

	reordered := command
	reordered.EdgeAppNetworks = []string{"gordon-app-other", "gordon-app-fixture"}
	assert.Error(t, store.RecordMigrationCutoverSubphase(context.Background(), reordered, domain.MigrationCutoverSubphaseBeforeCommit), "cutover identity requires exact checkpoint network order")

	require.NoError(t, store.CommitMigrationCutover(context.Background(), command))
	forged := command
	forged.EdgeAppNetworks = []string{"gordon-app-forged"}
	assert.Error(t, store.CommitMigrationCutover(context.Background(), forged), "forged edge app networks cannot match durable cutover identity")
}

func TestMigrationCheckpointPersistsOnlyAllowlistedCutoverIntent(t *testing.T) {
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	checkpoint := testMigrationCheckpoint()
	checkpoint.Phase = MigrationPhasePrepared
	checkpoint.TargetImage = "example.invalid/gordon:v2"
	checkpoint.OldServingPath = "old-monolith"
	checkpoint.RouteSnapshotGeneration = 7
	checkpoint.AppliedEdgeComponentID = "gordon-edge-migration-1-g1"
	checkpoint.PublicPortBindings = []MigrationPortBinding{{Role: "edge", HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 8080, Protocol: "tcp"}}
	require.NoError(t, store.Save(checkpoint))
	command := domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: domain.RuntimeCommandIdentity{Generation: checkpoint.ComponentGeneration}, LifecycleAction: domain.RuntimeComponentLifecycleActivate, TargetComponentRole: domain.ComponentRoleEdge, TargetComponentID: checkpoint.AppliedEdgeComponentID, PolicyDecisionID: "migration:" + checkpoint.MigrationID, OldServingComponentID: checkpoint.OldServingPath, FinalPortPublishes: []domain.ContainerPortPublish{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 8080, Protocol: domain.NetworkProtocolTCP}}}

	require.NoError(t, store.RecordMigrationCutoverSubphase(context.Background(), command, domain.MigrationCutoverSubphaseBeforePreparedRemove))
	loaded, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, domain.MigrationCutoverSubphaseBeforePreparedRemove, loaded.CutoverSubphase)
	assert.Error(t, store.RecordMigrationCutoverSubphase(context.Background(), command, domain.MigrationCutoverSubphase("engine error: secret=/host/socket")))

	stale := checkpoint
	require.NoError(t, store.Save(stale))
	loaded, err = store.Load()
	require.NoError(t, err)
	assert.Equal(t, domain.MigrationCutoverSubphaseBeforePreparedRemove, loaded.CutoverSubphase, "stale writers cannot erase a mutation intent")
}

func TestMigrationCheckpointRecordsSanitizedRetryableCutoverFailure(t *testing.T) {
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	checkpoint := testMigrationCheckpoint()
	checkpoint.Phase = MigrationPhasePrepared
	checkpoint.TargetImage = "example.invalid/gordon:v2"
	checkpoint.OldServingPath = "old-monolith"
	checkpoint.RouteSnapshotGeneration = 7
	checkpoint.AppliedEdgeComponentID = "gordon-edge-migration-1-g1"
	checkpoint.PublicPortBindings = []MigrationPortBinding{{Role: "edge", HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 8080, Protocol: "tcp"}}
	require.NoError(t, store.Save(checkpoint))
	command := domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: domain.RuntimeCommandIdentity{Generation: checkpoint.ComponentGeneration}, LifecycleAction: domain.RuntimeComponentLifecycleActivate, TargetComponentRole: domain.ComponentRoleEdge, TargetComponentID: checkpoint.AppliedEdgeComponentID, PolicyDecisionID: "migration:" + checkpoint.MigrationID, OldServingComponentID: checkpoint.OldServingPath, FinalPortPublishes: []domain.ContainerPortPublish{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 8080, Protocol: domain.NetworkProtocolTCP}}}

	require.NoError(t, store.RecordMigrationCutoverFailure(context.Background(), command, "listener_release_timeout", true))
	loaded, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, MigrationPhasePrepared, loaded.Phase)
	assert.Equal(t, "listener_release_timeout", loaded.CutoverFailureCode)
	assert.True(t, loaded.CutoverFailureRetryable)
	assert.NotContains(t, loaded.CutoverFailureCode, "8080", "status must never persist engine listener details")

	require.NoError(t, store.CommitMigrationCutover(context.Background(), command))
	loaded, err = store.Load()
	require.NoError(t, err)
	assert.Empty(t, loaded.CutoverFailureCode)
	assert.False(t, loaded.CutoverFailureRetryable)
}

func TestMigrationCheckpointSaveMergesMonotonicFactsAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "checkpoint.json")
	store, err := NewMigrationCheckpointStore(path)
	require.NoError(t, err)
	base := testMigrationCheckpoint()
	base.Phase = MigrationPhasePrepared
	base.TargetImage = "example.invalid/gordon:v2"
	base.BootstrapRuntimeEndpoint = "unix:///var/lib/gordon/migration/migration-1/runtime-control.sock"
	base.EnvFileReferences = []string{"/private/edge.env"}
	base.ConfigFileReferences = []string{"/private/edge.yaml"}
	require.NoError(t, store.Save(base))

	monolith := base
	monolith.PreparedComponents = []string{"gordon-control-migration-1-g1"}
	monolith.ConnectedEdgeNetworks = []string{"gordon-app"}
	monolith.RuntimeChannelTransferred = true
	require.NoError(t, store.Save(monolith))

	candidate := base
	candidate.RouteSnapshotGeneration = 7
	candidate.AppliedEdgeComponentID = "gordon-edge-migration-1-g1"
	encoded, err := json.Marshal(candidate)
	require.NoError(t, err)
	command := exec.Command(os.Args[0], "-test.run=^TestMigrationCheckpointSaveHelper$")
	command.Env = append(os.Environ(), "GORDON_CHECKPOINT_HELPER=1", "GORDON_CHECKPOINT_PATH="+path, "GORDON_CHECKPOINT_VALUE="+string(encoded))
	require.NoError(t, command.Run())

	loaded, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, uint64(7), loaded.RouteSnapshotGeneration)
	assert.Equal(t, candidate.AppliedEdgeComponentID, loaded.AppliedEdgeComponentID)
	assert.Equal(t, monolith.PreparedComponents, loaded.PreparedComponents)
	assert.Equal(t, monolith.ConnectedEdgeNetworks, loaded.ConnectedEdgeNetworks)
	assert.True(t, loaded.RuntimeChannelTransferred)
}

func TestMigrationCheckpointSaveRejectsImmutableChangesAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	store, err := NewMigrationCheckpointStore(path)
	require.NoError(t, err)
	checkpoint := testMigrationCheckpoint()
	checkpoint.Phase = MigrationPhasePrepared
	checkpoint.TargetImage = "example.invalid/gordon:v2"
	checkpoint.BootstrapRuntimeEndpoint = "unix:///var/lib/gordon/migration/migration-1/runtime-control.sock"
	checkpoint.BootstrapEdgeProbeEndpoint = "127.0.0.1:18080"
	checkpoint.EnvFileReferences = []string{"/private/edge.env"}
	checkpoint.ConfigFileReferences = []string{"/private/edge.yaml"}
	checkpoint.PreparedPortBindings = []MigrationPortBinding{{Role: "edge", HostIP: "127.0.0.1", HostPort: 18080, ContainerPort: 8080, Protocol: "tcp"}}
	require.NoError(t, store.Save(checkpoint))

	changed := checkpoint
	changed.TargetImage = "example.invalid/gordon:other"
	assert.Error(t, store.Save(changed))
	changed = checkpoint
	changed.EnvFileReferences = nil
	assert.Error(t, store.Save(changed))
	changed = checkpoint
	changed.ComponentGeneration++
	assert.Error(t, store.Save(changed))
	changed = checkpoint
	changed.Phase = MigrationPhasePlanned
	assert.Error(t, store.Save(changed))
	attested := checkpoint
	attested.RouteSnapshotGeneration = 7
	attested.AppliedEdgeComponentID = "gordon-edge-migration-1-g1"
	require.NoError(t, store.Save(attested))
	changed = checkpoint
	changed.RouteSnapshotGeneration = 8
	changed.AppliedEdgeComponentID = "gordon-edge-other-g1"
	assert.Error(t, store.Save(changed))
}

func TestMigrationCheckpointRejectsUnsafePermissionsAndReleasesLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	store, err := NewMigrationCheckpointStore(path)
	require.NoError(t, err)
	checkpoint := testMigrationCheckpoint()
	require.NoError(t, store.Save(checkpoint))
	require.NoError(t, os.Chmod(path, 0o644))
	assert.Error(t, store.Save(checkpoint))
	require.NoError(t, os.Chmod(path, 0o600))
	checkpoint.PreparedComponents = []string{"gordon-control-migration-1-g1"}
	require.NoError(t, store.Save(checkpoint))
}

func TestMigrationCheckpointLockIsReleasedAfterWriterCrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	store, err := NewMigrationCheckpointStore(path)
	require.NoError(t, err)
	require.NoError(t, store.Save(testMigrationCheckpoint()))
	command := exec.Command(os.Args[0], "-test.run=^TestMigrationCheckpointSaveHelper$")
	command.Env = append(os.Environ(), "GORDON_CHECKPOINT_LOCK_HELPER=1", "GORDON_CHECKPOINT_PATH="+path)
	output, err := command.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	scanner := bufio.NewScanner(output)
	require.True(t, scanner.Scan())
	require.Equal(t, "locked", scanner.Text())
	require.NoError(t, command.Process.Kill())
	require.Error(t, command.Wait())
	checkpoint, err := store.Load()
	require.NoError(t, err)
	checkpoint.PreparedComponents = []string{"gordon-control-migration-1-g1"}
	require.NoError(t, store.Save(*checkpoint))
}

func TestMigrationCheckpointSaveHelper(t *testing.T) {
	if os.Getenv("GORDON_CHECKPOINT_LOCK_HELPER") == "1" {
		store, err := NewMigrationCheckpointStore(os.Getenv("GORDON_CHECKPOINT_PATH"))
		if err != nil {
			os.Exit(2)
		}
		err = store.withLock(func() error {
			_, _ = fmt.Fprintln(os.Stdout, "locked")
			select {}
		})
		if err != nil {
			os.Exit(2)
		}
	}
	if os.Getenv("GORDON_CHECKPOINT_HELPER") != "1" {
		return
	}
	store, err := NewMigrationCheckpointStore(os.Getenv("GORDON_CHECKPOINT_PATH"))
	if err != nil {
		os.Exit(2)
	}
	var checkpoint MigrationCheckpoint
	if err := json.Unmarshal([]byte(os.Getenv("GORDON_CHECKPOINT_VALUE")), &checkpoint); err != nil {
		os.Exit(2)
	}
	if err := store.Save(checkpoint); err != nil {
		os.Exit(2)
	}
}

func TestMigrationCheckpointRejectsCorruptionAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.json")
	store, err := NewMigrationCheckpointStore(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o600))
	_, err = store.Load()
	assert.Error(t, err)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Symlink(filepath.Join(dir, "other"), path))
	_, err = store.Load()
	assert.Error(t, err)
	assert.False(t, errors.Is(err, os.ErrNotExist))
}
