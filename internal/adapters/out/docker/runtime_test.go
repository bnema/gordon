package docker

import (
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitForVolumeArchiveContainerIgnoresNilErrorBeforeStatus(t *testing.T) {
	statusCh := make(chan container.WaitResponse, 1)
	errCh := make(chan error, 1)
	errCh <- nil
	statusCh <- container.WaitResponse{StatusCode: 7}

	statusCode, err := waitForVolumeArchiveContainer(statusCh, errCh)

	require.NoError(t, err)
	assert.Equal(t, int64(7), statusCode)
}

func TestWaitForVolumeArchiveContainerHandlesClosedErrorChannelBeforeStatus(t *testing.T) {
	statusCh := make(chan container.WaitResponse, 1)
	errCh := make(chan error)
	close(errCh)
	statusCh <- container.WaitResponse{StatusCode: 3}

	statusCode, err := waitForVolumeArchiveContainer(statusCh, errCh)

	require.NoError(t, err)
	assert.Equal(t, int64(3), statusCode)
}

func TestWaitForVolumeArchiveContainerReturnsErrorChannelError(t *testing.T) {
	statusCh := make(chan container.WaitResponse)
	errCh := make(chan error, 1)
	wantErr := errors.New("wait failed")
	errCh <- wantErr

	statusCode, err := waitForVolumeArchiveContainer(statusCh, errCh)

	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, int64(0), statusCode)
}

func TestWaitForVolumeArchiveContainerErrorsWhenStatusChannelCloses(t *testing.T) {
	statusCh := make(chan container.WaitResponse)
	errCh := make(chan error)
	close(statusCh)
	close(errCh)

	statusCode, err := waitForVolumeArchiveContainer(statusCh, errCh)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "wait channel closed")
	assert.Equal(t, int64(0), statusCode)
}
