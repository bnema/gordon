package container

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

// reserveEphemeralAddr binds on 127.0.0.1:0, captures the assigned port, closes
// the listener, and returns the address string. The port is guaranteed to be
// unreachable (no process listening) when the probe runs.
func reserveEphemeralAddr(t *testing.T) (ip string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().(*net.TCPAddr)
	ln.Close()
	return addr.IP.String(), addr.Port
}

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

func TestService_WaitForReady_AutoCascadeUsesDefaultHTTPAliveProbeWhenNoHealthcheckAndNoHealthLabel(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := NewService(runtime, nil, nil, nil, Config{
		ReadinessMode:    "auto",
		HTTPProbeTimeout: 50 * time.Millisecond,
	})

	ctx := testContext()
	containerID := "container-1"

	containerConfig := &domain.ContainerConfig{
		Ports:  []int{8080},
		Labels: map[string]string{},
	}

	// Reserve an ephemeral loopback port that is guaranteed to be unreachable.
	probeIP, probePort := reserveEphemeralAddr(t)

	// Initial running poll
	runtime.EXPECT().IsContainerRunning(mock.Anything, containerID).Return(true, nil).Once()
	// No Docker healthcheck
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, containerID).Return("", false, nil).Once()
	// Cascade resolves container endpoint for default HTTP alive probe
	runtime.EXPECT().GetContainerNetworkInfo(mock.Anything, containerID).Return(probeIP, probePort, nil).Once()
	// Host port binding resolution — return the same loopback addr so probe hits it
	runtime.EXPECT().GetContainerPort(mock.Anything, containerID, probePort).Return(probePort, nil).Once()

	// Default HTTP alive probe will try to connect — which will fail since the port is closed.

	err := svc.waitForReady(ctx, containerID, containerConfig)
	// Expect HTTP alive probe timeout (no server listening)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP alive probe timeout")
}

func TestService_WaitForReady_AutoCascadeUsesHTTPProbeWhenHealthLabelSet(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := NewService(runtime, nil, nil, nil, Config{
		ReadinessMode:    "auto",
		HTTPProbeTimeout: 50 * time.Millisecond,
	})

	ctx := testContext()
	containerID := "container-1"

	containerConfig := &domain.ContainerConfig{
		Ports: []int{8080},
		Labels: map[string]string{
			domain.LabelHealth: "/healthz",
		},
	}

	// Reserve an ephemeral loopback port that is guaranteed to be unreachable.
	probeIP, probePort := reserveEphemeralAddr(t)

	// Initial running poll
	runtime.EXPECT().IsContainerRunning(mock.Anything, containerID).Return(true, nil).Once()
	// No Docker healthcheck
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, containerID).Return("", false, nil).Once()
	// Cascade resolves container endpoint for HTTP probe
	runtime.EXPECT().GetContainerNetworkInfo(mock.Anything, containerID).Return(probeIP, probePort, nil).Once()
	// Host port binding resolution — return the same loopback addr so probe hits it
	runtime.EXPECT().GetContainerPort(mock.Anything, containerID, probePort).Return(probePort, nil).Once()

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
	// Network info fails for default HTTP alive probe, then again for TCP fallback before delay
	runtime.EXPECT().GetContainerNetworkInfo(mock.Anything, containerID).Return("", 0, assert.AnError).Twice()
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
