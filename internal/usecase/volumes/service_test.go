package volumes

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

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

func TestService_PruneVolumes_RemovesOrphaned(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)

	vols := []*domain.VolumeInfo{
		{Name: "vol-in-use", InUse: true, Containers: []string{"web"}, Labels: map[string]string{domain.LabelManaged: "true"}},
		{Name: "vol-orphaned", InUse: false, Size: 1024, Labels: map[string]string{domain.LabelManaged: "true"}},
	}
	runtime.EXPECT().ListVolumes(context.Background()).Return(vols, nil)
	runtime.EXPECT().RemoveVolume(context.Background(), "vol-orphaned", false).Return(nil)

	svc := NewService(runtime)
	report, removed, err := svc.PruneVolumes(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, 1, report.VolumesRemoved)
	assert.Equal(t, int64(1024), report.SpaceReclaimed)
	assert.Len(t, removed, 1)
	assert.Equal(t, "vol-orphaned", removed[0].Name)
}

func TestService_PruneVolumes_DryRunDoesNotRemove(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)

	vols := []*domain.VolumeInfo{
		{Name: "vol-orphaned", InUse: false, Size: 2048, Labels: map[string]string{domain.LabelManaged: "true"}},
	}
	runtime.EXPECT().ListVolumes(context.Background()).Return(vols, nil)

	svc := NewService(runtime)
	report, removed, err := svc.PruneVolumes(context.Background(), true)
	require.NoError(t, err)
	assert.Equal(t, 1, report.VolumesRemoved)
	assert.Equal(t, int64(2048), report.SpaceReclaimed)
	assert.Len(t, removed, 1)
}

func TestService_PruneVolumes_OnlyRemovesUnusedGordonManagedVolumes(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)

	vols := []*domain.VolumeInfo{
		{Name: "gordon-used", InUse: true, Size: 100, Labels: map[string]string{domain.LabelManaged: "true"}},
		{Name: "gordon-unused", InUse: false, Size: 200, Labels: map[string]string{domain.LabelManaged: "true"}},
		{Name: "other-unused", InUse: false, Size: 300},
		{Name: "other-labelled-false", InUse: false, Size: 400, Labels: map[string]string{domain.LabelManaged: "false"}},
		{Name: "other-used", InUse: true, Size: 500},
	}
	runtime.EXPECT().ListVolumes(context.Background()).Return(vols, nil)
	runtime.EXPECT().RemoveVolume(context.Background(), "gordon-unused", false).Return(nil)

	svc := NewService(runtime)
	report, removed, err := svc.PruneVolumes(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, 1, report.VolumesRemoved)
	assert.Equal(t, int64(200), report.SpaceReclaimed)
	require.Len(t, removed, 1)
	assert.Equal(t, "gordon-unused", removed[0].Name)
}

func TestService_PruneVolumes_DryRunReportsOnlyUnusedGordonManagedVolumes(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)

	vols := []*domain.VolumeInfo{
		{Name: "gordon-used", InUse: true, Size: 100, Labels: map[string]string{domain.LabelManaged: "true"}},
		{Name: "gordon-unused", InUse: false, Size: 200, Labels: map[string]string{domain.LabelManaged: "true"}},
		{Name: "other-unused", InUse: false, Size: 300},
	}
	runtime.EXPECT().ListVolumes(context.Background()).Return(vols, nil)

	svc := NewService(runtime)
	report, removed, err := svc.PruneVolumes(context.Background(), true)
	require.NoError(t, err)
	assert.Equal(t, 1, report.VolumesRemoved)
	assert.Equal(t, int64(200), report.SpaceReclaimed)
	require.Len(t, removed, 1)
	assert.Equal(t, "gordon-unused", removed[0].Name)
}

func TestService_PruneVolumes_AllInUse(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)

	vols := []*domain.VolumeInfo{
		{Name: "vol1", InUse: true, Containers: []string{"web"}},
	}
	runtime.EXPECT().ListVolumes(context.Background()).Return(vols, nil)

	svc := NewService(runtime)
	report, removed, err := svc.PruneVolumes(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, 0, report.VolumesRemoved)
	assert.Empty(t, removed)
}

func TestService_ListVolumes_PropagatesError(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().ListVolumes(context.Background()).Return(nil, fmt.Errorf("connection refused"))

	svc := NewService(runtime)
	_, err := svc.ListVolumes(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")
}

func TestService_PruneVolumes_SkipsFailedRemoval(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)

	vols := []*domain.VolumeInfo{
		{Name: "vol-fail", InUse: false, Size: 1024, Labels: map[string]string{domain.LabelManaged: "true"}},
		{Name: "vol-ok", InUse: false, Size: 2048, Labels: map[string]string{domain.LabelManaged: "true"}},
	}
	runtime.EXPECT().ListVolumes(context.Background()).Return(vols, nil)
	runtime.EXPECT().RemoveVolume(context.Background(), "vol-fail", false).Return(fmt.Errorf("volume in use"))
	runtime.EXPECT().RemoveVolume(context.Background(), "vol-ok", false).Return(nil)

	svc := NewService(runtime)
	report, removed, err := svc.PruneVolumes(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, 1, report.VolumesRemoved)
	assert.Equal(t, int64(2048), report.SpaceReclaimed)
	assert.Len(t, removed, 1)
	assert.Equal(t, "vol-ok", removed[0].Name)
}
