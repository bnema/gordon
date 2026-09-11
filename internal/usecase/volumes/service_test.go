package volumes

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

// appVolumeLabels are the runtime labels a stamped app volume carries.
func appVolumeLabels(app, service string) map[string]string {
	return map[string]string{
		domain.LabelManaged:    "true",
		domain.LabelApp:        app,
		domain.LabelAppID:      app + "-uuid",
		domain.LabelAppService: service,
	}
}

// newPruneService wires a volume service against a scripted snapshot and
// runtime inventory.
func newPruneService(
	t *testing.T,
	volumes []*domain.VolumeInfo,
	snapshot *domain.PruneProtectionSnapshot,
	inventoryGaps ...domain.InventoryGap,
) (*Service, *outmocks.MockContainerRuntime, *outmocks.MockPruneRuntime, *outmocks.MockPruneProtectionStore, *outmocks.MockGCBarrier) {
	t.Helper()
	runtime := outmocks.NewMockContainerRuntime(t)
	protection := outmocks.NewMockPruneProtectionStore(t)
	pruneRuntime := outmocks.NewMockPruneRuntime(t)
	barrier := outmocks.NewMockGCBarrier(t)

	if snapshot == nil {
		snapshot = &domain.PruneProtectionSnapshot{}
	}
	protection.EXPECT().ProtectionSnapshot(context.Background()).Return(snapshot, nil)
	pruneRuntime.EXPECT().InventoryRuntime(context.Background()).Return(&domain.RuntimeInventory{
		Volumes: volumes,
		Gaps:    inventoryGaps,
	}, nil)

	lease := outmocks.NewMockGCLease(t)
	barrier.EXPECT().AcquireExclusive(context.Background()).Return(lease, nil)
	lease.EXPECT().Release()

	svc := NewService(runtime).WithPrunePorts(protection, pruneRuntime, barrier)
	return svc, runtime, pruneRuntime, protection, barrier
}

func TestService_ListVolumes(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)

	vols := []*domain.VolumeInfo{
		{Name: "vol1", InUse: true, Containers: []string{"web"}},
		{Name: "vol2", InUse: false},
	}
	runtime.EXPECT().ListVolumes(context.Background()).Return(vols, nil)

	svc := NewService(runtime)
	result, err := svc.ListVolumes(context.Background())
	require.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestService_ListVolumes_PropagatesError(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().ListVolumes(context.Background()).Return(nil, fmt.Errorf("connection refused"))

	svc := NewService(runtime)
	_, err := svc.ListVolumes(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")
}

// TestService_PruneVolumes_RemovesOnlyExplicitlyReleasedVolumes is the
// central safety case: managed, app-labelled, and legacy volumes all
// survive; only a released, label-agreeing, unused volume is removed.
func TestService_PruneVolumes_RemovesOnlyExplicitlyReleasedVolumes(t *testing.T) {
	const released = "gordon-shop--web--vol--data"
	volumes := []*domain.VolumeInfo{
		{Name: released, Size: 1024, Labels: appVolumeLabels("shop", "web")},
		{Name: "gordon-shop--api--vol--data", Size: 200, Labels: appVolumeLabels("shop", "api")},
		{Name: "gordon-old--web--vol--data", Size: 300, Labels: appVolumeLabels("old", "web")},
		{Name: "gordon-legacy", Size: 400, Labels: map[string]string{domain.LabelManaged: "true"}},
		{Name: "pgdata", Size: 500},
		{Name: "gordon-shop--cache--vol--data", Size: 600, Labels: appVolumeLabels("shop", "web"), InUse: true},
	}
	snapshot := &domain.PruneProtectionSnapshot{VolumeClaims: []domain.VolumeClaim{
		{Name: released, App: "shop", AppID: "shop-uuid", Service: "web", State: domain.VolumeClaimReleased},
		{Name: "gordon-shop--api--vol--data", App: "shop", AppID: "shop-uuid", Service: "api", State: domain.VolumeClaimAttached},
		{Name: "gordon-old--web--vol--data", App: "old", AppID: "old-uuid", Service: "web", State: domain.VolumeClaimRetained},
		{Name: "gordon-shop--cache--vol--data", App: "shop", AppID: "shop-uuid", Service: "web", State: domain.VolumeClaimAttached},
	}}

	svc, _, pruneRuntime, _, _ := newPruneService(t, volumes, snapshot)
	pruneRuntime.EXPECT().RemoveVolumeExact(context.Background(), domain.RuntimeVolumeRef{Name: released}).Return(nil)

	report, removed, err := svc.PruneVolumes(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, 1, report.VolumesRemoved)
	assert.Equal(t, int64(1024), report.SpaceReclaimed)
	require.Len(t, removed, 1)
	assert.Equal(t, released, removed[0].Name)
	assert.True(t, report.Plan.Applied)

	protected := report.Plan.CountByVerdict(domain.PruneVerdictProtected)
	assert.GreaterOrEqual(t, protected, 4, "attached, retained, legacy, unknown and in-use volumes must be protected")
}

func TestService_PruneVolumes_UnknownAndContradictoryVolumesSurvive(t *testing.T) {
	volumes := []*domain.VolumeInfo{
		// App labels but no durable record: ownership history missing.
		{Name: "gordon-ghost--web--vol--data", Labels: appVolumeLabels("ghost", "web")},
		// Durable released record but contradicting labels.
		{Name: "gordon-shop--web--vol--other", Labels: appVolumeLabels("other", "web")},
	}
	snapshot := &domain.PruneProtectionSnapshot{VolumeClaims: []domain.VolumeClaim{
		{Name: "gordon-shop--web--vol--other", App: "shop", AppID: "shop-uuid", Service: "web", State: domain.VolumeClaimReleased},
	}}

	svc, _, pruneRuntime, _, _ := newPruneService(t, volumes, snapshot)
	pruneRuntime.EXPECT().RemoveVolumeExact(mock.Anything, mock.Anything).Maybe().Return(nil)

	report, removed, err := svc.PruneVolumes(context.Background(), false)
	require.NoError(t, err)
	assert.Zero(t, report.VolumesRemoved)
	assert.Empty(t, removed)
	assert.Equal(t, 2, report.Plan.CountByVerdict(domain.PruneVerdictUnknown))
}

func TestService_PruneVolumes_DryRunPlansWithoutDeleting(t *testing.T) {
	const released = "gordon-shop--web--vol--data"
	volumes := []*domain.VolumeInfo{{Name: released, Size: 2048, Labels: appVolumeLabels("shop", "web")}}
	snapshot := &domain.PruneProtectionSnapshot{VolumeClaims: []domain.VolumeClaim{
		{Name: released, App: "shop", AppID: "shop-uuid", Service: "web", State: domain.VolumeClaimReleased},
	}}

	svc, _, pruneRuntime, _, _ := newPruneService(t, volumes, snapshot)
	pruneRuntime.EXPECT().RemoveVolumeExact(mock.Anything, mock.Anything).Maybe().Return(nil)

	report, removed, err := svc.PruneVolumes(context.Background(), true)
	require.NoError(t, err)
	assert.Zero(t, report.VolumesRemoved)
	assert.Empty(t, removed, "a dry run must report only what would be removed")
	assert.False(t, report.Plan.Applied)
	assert.Equal(t, 1, report.Plan.CountByVerdict(domain.PruneVerdictEligible))

	// The dry-run plan and the executed plan must agree.
	executed, _, err := svc.PruneVolumes(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, report.Plan.Candidates, executed.Plan.Candidates)
}

func TestService_PruneVolumes_ZeroDeletionSucceeds(t *testing.T) {
	volumes := []*domain.VolumeInfo{
		{Name: "pgdata"},
		{Name: "gordon-shop--web--vol--data", Labels: appVolumeLabels("shop", "web"), InUse: true},
	}
	svc, _, _, _, _ := newPruneService(t, volumes, nil)

	report, removed, err := svc.PruneVolumes(context.Background(), false)
	require.NoError(t, err, "with current metadata a zero-deletion prune is valid")
	assert.Zero(t, report.VolumesRemoved)
	assert.Empty(t, removed)
	assert.False(t, report.Plan.ReclaimedKnown)
}

func TestService_PruneVolumes_FailedRemovalIsIsolated(t *testing.T) {
	first := "gordon-a--web--vol--data"
	second := "gordon-b--web--vol--data"
	volumes := []*domain.VolumeInfo{
		{Name: first, Size: 1024, Labels: appVolumeLabels("a", "web")},
		{Name: second, Size: 2048, Labels: appVolumeLabels("b", "web")},
	}
	snapshot := &domain.PruneProtectionSnapshot{VolumeClaims: []domain.VolumeClaim{
		{Name: first, App: "a", AppID: "a-uuid", Service: "web", State: domain.VolumeClaimReleased},
		{Name: second, App: "b", AppID: "b-uuid", Service: "web", State: domain.VolumeClaimReleased},
	}}

	svc, _, pruneRuntime, _, _ := newPruneService(t, volumes, snapshot)
	pruneRuntime.EXPECT().RemoveVolumeExact(context.Background(), domain.RuntimeVolumeRef{Name: first}).Return(fmt.Errorf("volume in use"))
	pruneRuntime.EXPECT().RemoveVolumeExact(context.Background(), domain.RuntimeVolumeRef{Name: second}).Return(nil)

	report, removed, err := svc.PruneVolumes(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, 1, report.VolumesRemoved)
	assert.Equal(t, int64(2048), report.SpaceReclaimed)
	require.Len(t, removed, 1)
	assert.Equal(t, second, removed[0].Name)
	require.Len(t, report.Plan.Failures, 1)
	assert.Equal(t, first, report.Plan.Failures[0].Ref)
}

func TestService_PruneVolumes_IncompleteInventoryProtectsEverything(t *testing.T) {
	const released = "gordon-shop--web--vol--data"
	volumes := []*domain.VolumeInfo{{Name: released, Labels: appVolumeLabels("shop", "web")}}
	snapshot := &domain.PruneProtectionSnapshot{VolumeClaims: []domain.VolumeClaim{
		{Name: released, App: "shop", AppID: "shop-uuid", Service: "web", State: domain.VolumeClaimReleased},
	}}
	gap := domain.InventoryGap{
		Source: domain.InventorySourceRuntimeContainers,
		Reason: domain.PruneReasonUnknownContainerUse,
		Detail: "list containers failed",
	}

	svc, _, _, _, _ := newPruneService(t, volumes, snapshot, gap)
	report, removed, err := svc.PruneVolumes(context.Background(), false)
	require.NoError(t, err)
	assert.Empty(t, removed)
	assert.Equal(t, 1, report.Plan.CountByVerdict(domain.PruneVerdictUnknown))
	require.Len(t, report.Plan.Gaps, 1)
}

func TestService_PruneVolumes_RequiresPrunePorts(t *testing.T) {
	svc := NewService(outmocks.NewMockContainerRuntime(t))
	_, _, err := svc.PruneVolumes(context.Background(), false)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrPruneDisabled)
}

func TestService_PruneVolumes_ProtectionSnapshotFailureFailsClosed(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	protection := outmocks.NewMockPruneProtectionStore(t)
	pruneRuntime := outmocks.NewMockPruneRuntime(t)
	barrier := outmocks.NewMockGCBarrier(t)
	lease := outmocks.NewMockGCLease(t)
	barrier.EXPECT().AcquireExclusive(context.Background()).Return(lease, nil)
	lease.EXPECT().Release()
	protection.EXPECT().ProtectionSnapshot(context.Background()).Return(nil, fmt.Errorf("unreadable state"))

	svc := NewService(runtime).WithPrunePorts(protection, pruneRuntime, barrier)
	_, _, err := svc.PruneVolumes(context.Background(), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protection snapshot")
}
