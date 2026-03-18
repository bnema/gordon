package volumes

import (
	"context"
	"testing"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		{Name: "vol-in-use", InUse: true, Containers: []string{"web"}},
		{Name: "vol-orphaned", InUse: false, Size: 1024},
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
		{Name: "vol-orphaned", InUse: false, Size: 2048},
	}
	runtime.EXPECT().ListVolumes(context.Background()).Return(vols, nil)

	svc := NewService(runtime)
	report, removed, err := svc.PruneVolumes(context.Background(), true)
	require.NoError(t, err)
	assert.Equal(t, 1, report.VolumesRemoved)
	assert.Equal(t, int64(2048), report.SpaceReclaimed)
	assert.Len(t, removed, 1)
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
