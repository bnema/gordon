package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
)

type fakeVolumesClient struct {
	listResp  []dto.Volume
	pruneResp *dto.VolumePruneResponse
	listErr   error
	pruneErr  error
}

func (f *fakeVolumesClient) ListVolumes(_ context.Context) ([]dto.Volume, error) {
	return f.listResp, f.listErr
}

func (f *fakeVolumesClient) PruneVolumes(_ context.Context, _ dto.VolumePruneRequest) (*dto.VolumePruneResponse, error) {
	return f.pruneResp, f.pruneErr
}

func TestRunVolumesList_ShowsVolumes(t *testing.T) {
	client := &fakeVolumesClient{
		listResp: []dto.Volume{
			{Name: "db-data", InUse: true, Containers: []string{"postgres"}, Size: 1024 * 1024},
			{Name: "orphaned-vol", InUse: false, Size: 512},
		},
	}

	var out bytes.Buffer
	err := runVolumesList(context.Background(), client, &out, false)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "db-data")
	assert.Contains(t, out.String(), "orphaned-vol")
}

func TestRunVolumesList_JSON(t *testing.T) {
	client := &fakeVolumesClient{
		listResp: []dto.Volume{
			{Name: "vol1", InUse: true},
		},
	}

	var out bytes.Buffer
	err := runVolumesList(context.Background(), client, &out, true)
	require.NoError(t, err)
	assert.Contains(t, out.String(), `"name"`)
}

func TestRunVolumesPrune_DryRun(t *testing.T) {
	client := &fakeVolumesClient{
		pruneResp: &dto.VolumePruneResponse{
			VolumesRemoved: 2,
			SpaceReclaimed: 4096,
			Volumes: []dto.Volume{
				{Name: "orphan1", Size: 2048},
				{Name: "orphan2", Size: 2048},
			},
		},
	}

	var out bytes.Buffer
	err := runVolumesPrune(context.Background(), client, volumesPruneOptions{DryRun: true}, &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "orphan1")
}
