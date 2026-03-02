package container

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
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

	err := svc.waitForReady(ctx, containerID, nil)
	assert.NoError(t, err)
}

func TestService_WaitForReady_AutoCascadeUsesTCPWhenNoHealthcheckAndNoHealthLabel(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := NewService(runtime, nil, nil, nil, Config{
		ReadinessMode: "auto",
	})

	ctx := testContext()
	containerID := "container-1"

	containerConfig := &domain.ContainerConfig{
		Ports:  []int{8080},
		Labels: map[string]string{},
	}

	// Initial running poll
	runtime.EXPECT().IsContainerRunning(mock.Anything, containerID).Return(true, nil).Once()
	// No Docker healthcheck
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, containerID).Return("", false, nil).Once()
	// Cascade resolves container endpoint for TCP probe
	runtime.EXPECT().GetContainerNetworkInfo(mock.Anything, containerID).Return("172.17.0.5", 8080, nil).Once()

	// TCP probe will try to connect to 172.17.0.5:8080 — which will fail since
	// there's nothing listening. Use a short health timeout so the test doesn't hang.
	svc.mu.Lock()
	svc.config.HealthTimeout = 50 * time.Millisecond
	svc.mu.Unlock()

	err := svc.waitForReady(ctx, containerID, containerConfig)
	// Expect TCP probe timeout (no server listening at 172.17.0.5:8080)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TCP probe timeout")
}

func TestService_WaitForReady_AutoCascadeUsesHTTPProbeWhenHealthLabelSet(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := NewService(runtime, nil, nil, nil, Config{
		ReadinessMode: "auto",
		HealthTimeout: 50 * time.Millisecond,
	})

	ctx := testContext()
	containerID := "container-1"

	containerConfig := &domain.ContainerConfig{
		Ports: []int{8080},
		Labels: map[string]string{
			domain.LabelHealth: "/healthz",
		},
	}

	// Initial running poll
	runtime.EXPECT().IsContainerRunning(mock.Anything, containerID).Return(true, nil).Once()
	// No Docker healthcheck
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, containerID).Return("", false, nil).Once()
	// Cascade resolves container endpoint for HTTP probe
	runtime.EXPECT().GetContainerNetworkInfo(mock.Anything, containerID).Return("172.17.0.5", 8080, nil).Once()

	err := svc.waitForReady(ctx, containerID, containerConfig)
	// Expect HTTP probe timeout (no server listening)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP probe timeout")
}

func TestService_WaitForReady_AutoCascadeUsesHealthcheckWhenPresent(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := NewService(runtime, nil, nil, nil, Config{
		ReadinessMode: "auto",
		HealthTimeout: 50 * time.Millisecond,
	})

	ctx := testContext()
	containerID := "container-1"

	containerConfig := &domain.ContainerConfig{
		Ports: []int{8080},
		Labels: map[string]string{
			domain.LabelHealth: "/healthz",
		},
	}

	// Initial running poll
	runtime.EXPECT().IsContainerRunning(mock.Anything, containerID).Return(true, nil).Once()
	// Docker healthcheck IS present — cascade detects it, then waitForHealthy polls it
	// First call: cascade detection (hasHealthcheck=true)
	// Second call: waitForHealthy loop (status=healthy)
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, containerID).Return("starting", true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, containerID).Return("healthy", true, nil).Once()

	err := svc.waitForReady(ctx, containerID, containerConfig)
	assert.NoError(t, err)
}

func TestService_WaitForReady_AutoCascadeFallsToDelayWhenNoEndpoint(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := NewService(runtime, nil, nil, nil, Config{
		ReadinessMode:  "auto",
		ReadinessDelay: time.Millisecond,
	})

	ctx := testContext()
	containerID := "container-1"

	// Container has ports but network info is unavailable
	containerConfig := &domain.ContainerConfig{
		Ports:  []int{8080},
		Labels: map[string]string{},
	}

	// Initial running poll
	runtime.EXPECT().IsContainerRunning(mock.Anything, containerID).Return(true, nil).Once()
	// No Docker healthcheck
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, containerID).Return("", false, nil).Once()
	// Network info fails — can't do TCP probe, fall through to delay
	runtime.EXPECT().GetContainerNetworkInfo(mock.Anything, containerID).Return("", 0, assert.AnError).Once()
	// Delay-mode fallback verification
	runtime.EXPECT().IsContainerRunning(mock.Anything, containerID).Return(true, nil).Once()

	err := svc.waitForReady(ctx, containerID, containerConfig)
	assert.NoError(t, err)
}

func TestService_WaitForReady_ExplicitDelayModeIgnoresCascade(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := NewService(runtime, nil, nil, nil, Config{
		ReadinessMode:  "delay",
		ReadinessDelay: time.Millisecond,
	})

	ctx := testContext()
	containerID := "container-1"

	// Even with health label and ports, "delay" mode skips cascade entirely
	containerConfig := &domain.ContainerConfig{
		Ports: []int{8080},
		Labels: map[string]string{
			domain.LabelHealth: "/healthz",
		},
	}

	// Initial running poll
	runtime.EXPECT().IsContainerRunning(mock.Anything, containerID).Return(true, nil).Once()
	// No GetContainerHealthStatus call — delay mode skips it
	// Delay-mode verification
	runtime.EXPECT().IsContainerRunning(mock.Anything, containerID).Return(true, nil).Once()

	err := svc.waitForReady(ctx, containerID, containerConfig)
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
