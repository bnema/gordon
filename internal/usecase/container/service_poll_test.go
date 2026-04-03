package container

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestPollContainerRunning(t *testing.T) {
	t.Run("container starts running immediately", func(t *testing.T) {
		runtime := mocks.NewMockContainerRuntime(t)
		svc := NewService(runtime, nil, nil, nil, Config{}, nil)
		ctx := testContext()

		runtime.EXPECT().IsContainerRunning(ctx, "ctr-1").Return(true, nil).Once()

		err := svc.pollContainerRunning(ctx, "ctr-1")
		require.NoError(t, err)
	})

	t.Run("container exits immediately with non-zero exit code", func(t *testing.T) {
		runtime := mocks.NewMockContainerRuntime(t)
		svc := NewService(runtime, nil, nil, nil, Config{}, nil)
		ctx := testContext()

		runtime.EXPECT().IsContainerRunning(ctx, "ctr-2").Return(false, nil).Once()
		runtime.EXPECT().InspectContainer(ctx, "ctr-2").Return(&domain.Container{
			ID:       "ctr-2",
			Status:   string(domain.ContainerStatusExited),
			ExitCode: 127,
		}, nil).Once()

		err := svc.pollContainerRunning(ctx, "ctr-2")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "127")
		assert.Contains(t, err.Error(), "exited")
		assert.True(t, errors.Is(err, domain.ErrContainerExited))
	})

	t.Run("container starts running after one failed poll", func(t *testing.T) {
		runtime := mocks.NewMockContainerRuntime(t)
		svc := NewService(runtime, nil, nil, nil, Config{}, nil)
		ctx := testContext()

		// First poll: not yet running, still in "created" state (not exited)
		runtime.EXPECT().IsContainerRunning(ctx, "ctr-3").Return(false, nil).Once()
		runtime.EXPECT().InspectContainer(ctx, "ctr-3").Return(&domain.Container{
			ID:     "ctr-3",
			Status: string(domain.ContainerStatusCreated),
		}, nil).Once()

		// Second poll: running
		runtime.EXPECT().IsContainerRunning(ctx, "ctr-3").Return(true, nil).Once()

		err := svc.pollContainerRunning(ctx, "ctr-3")
		require.NoError(t, err)
	})
}
