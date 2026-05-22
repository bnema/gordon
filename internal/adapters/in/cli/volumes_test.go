package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	climocks "github.com/bnema/gordon/internal/adapters/in/cli/mocks"
)

func TestRunVolumesList_ShowsVolumes(t *testing.T) {
	clientMock := climocks.NewMockvolumesClient(t)
	clientMock.EXPECT().ListVolumes(context.Background()).Return([]dto.Volume{
		{Name: "db-data", InUse: true, Containers: []string{"postgres"}, Size: 1024 * 1024},
		{Name: "orphaned-vol", InUse: false, Size: 512},
	}, nil).Once()

	var out bytes.Buffer
	err := runVolumesList(context.Background(), clientMock, &out, false)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "db-data")
	assert.Contains(t, out.String(), "orphaned-vol")
}

func TestRunVolumesList_JSON(t *testing.T) {
	clientMock := climocks.NewMockvolumesClient(t)
	clientMock.EXPECT().ListVolumes(context.Background()).Return([]dto.Volume{
		{Name: "vol1", InUse: true},
	}, nil).Once()

	var out bytes.Buffer
	err := runVolumesList(context.Background(), clientMock, &out, true)
	require.NoError(t, err)
	assert.Contains(t, out.String(), `"name"`)
}

func TestRunVolumesPrune_JSONExecutesActualPrune(t *testing.T) {
	clientMock := climocks.NewMockvolumesClient(t)
	resp := &dto.VolumePruneResponse{
		VolumesRemoved: 1,
		SpaceReclaimed: 1024,
		Volumes:        []dto.Volume{{Name: "orphan1", Size: 1024}},
	}
	previewCall := clientMock.EXPECT().PruneVolumes(context.Background(), dto.VolumePruneRequest{DryRun: true}).Return(resp, nil).Once()
	pruneCall := clientMock.EXPECT().PruneVolumes(context.Background(), dto.VolumePruneRequest{DryRun: false}).Return(resp, nil).Once()
	mock.InOrder(previewCall, pruneCall)

	var out bytes.Buffer
	err := runVolumesPrune(context.Background(), clientMock, volumesPruneOptions{Json: true}, &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), `"volumes_removed"`)
}

func TestRunVolumesPrune_DryRun(t *testing.T) {
	clientMock := climocks.NewMockvolumesClient(t)
	clientMock.EXPECT().PruneVolumes(context.Background(), dto.VolumePruneRequest{DryRun: true}).Return(&dto.VolumePruneResponse{
		VolumesRemoved: 2,
		SpaceReclaimed: 4096,
		Volumes: []dto.Volume{
			{Name: "orphan1", Size: 2048},
			{Name: "orphan2", Size: 2048},
		},
	}, nil).Once()

	var out bytes.Buffer
	err := runVolumesPrune(context.Background(), clientMock, volumesPruneOptions{DryRun: true}, &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "orphan1")
}
