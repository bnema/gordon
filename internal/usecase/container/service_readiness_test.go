package container

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/bnema/gordon/internal/boundaries/out/mocks"
)

func TestService_WaitForReady_AutoFallsBackToDelayWhenNoHealthcheck(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := NewService(runtime, nil, nil, nil, Config{
		ReadinessMode:  "auto",
		ReadinessDelay: time.Millisecond,
	})

	ctx := testContext()
	containerID := "container-1"

	// Initial running poll
	runtime.EXPECT().IsContainerRunning(mock.Anything, containerID).Return(true, nil).Once()
	// Auto mode probes for healthcheck support first
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, containerID).Return("", false, nil).Once()
	// Delay-mode fallback verification
	runtime.EXPECT().IsContainerRunning(mock.Anything, containerID).Return(true, nil).Once()

	err := svc.waitForReady(ctx, containerID)
	assert.NoError(t, err)
}

func TestService_WaitForHealthy_EmptyStatusIsReportedAsStarting(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := NewService(runtime, nil, nil, nil, Config{})

	ctx := testContext()
	containerID := "container-1"

	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, containerID).Return("", true, nil).Once()

	err := svc.waitForHealthy(ctx, containerID, 10*time.Millisecond)
	assert.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "last status: starting"))
}
