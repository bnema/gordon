package container

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func newTestService(runtime *mocks.MockContainerRuntime) *Service {
	return &Service{
		runtime:     runtime,
		config:      Config{},
		containers:  make(map[string]*domain.Container),
		attachments: make(map[string][]string),
	}
}

func monitorTestContext() context.Context {
	return zerowrap.WithCtx(context.Background(), zerowrap.Default())
}

func TestMonitor_RestartsCrashedContainer(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := newTestService(runtime)

	// Track a container.
	svc.containers["app.example.com"] = &domain.Container{
		ID:    "ctr-1",
		Image: "myapp:latest",
	}

	// Inspect returns exited with non-zero exit code.
	runtime.EXPECT().InspectContainer(mock.Anything, "ctr-1").Return(&domain.Container{
		ID:       "ctr-1",
		Status:   string(domain.ContainerStatusExited),
		ExitCode: 1,
	}, nil)

	// Monitor should restart it.
	runtime.EXPECT().StartContainer(mock.Anything, "ctr-1").Return(nil)

	m := newMonitor(svc)
	m.check(monitorTestContext())

	runtime.AssertExpectations(t)
}

func TestMonitor_SkipsGracefulExit(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := newTestService(runtime)

	svc.containers["app.example.com"] = &domain.Container{
		ID:    "ctr-1",
		Image: "myapp:latest",
	}

	// Exited with code 0 — graceful stop.
	runtime.EXPECT().InspectContainer(mock.Anything, "ctr-1").Return(&domain.Container{
		ID:       "ctr-1",
		Status:   string(domain.ContainerStatusExited),
		ExitCode: 0,
	}, nil)

	// Should NOT call StartContainer.

	m := newMonitor(svc)
	m.check(monitorTestContext())

	runtime.AssertExpectations(t)
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
}

func TestMonitor_SkipsRunningContainer(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := newTestService(runtime)

	svc.containers["app.example.com"] = &domain.Container{
		ID:    "ctr-1",
		Image: "myapp:latest",
	}

	runtime.EXPECT().InspectContainer(mock.Anything, "ctr-1").Return(&domain.Container{
		ID:     "ctr-1",
		Status: string(domain.ContainerStatusRunning),
	}, nil)

	// Running container — check health status.
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "ctr-1").Return("healthy", true, nil)

	m := newMonitor(svc)
	m.check(monitorTestContext())

	runtime.AssertExpectations(t)
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
}

func TestMonitor_RestartsUnhealthyContainer(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := newTestService(runtime)

	svc.containers["app.example.com"] = &domain.Container{
		ID:    "ctr-1",
		Image: "myapp:latest",
	}

	runtime.EXPECT().InspectContainer(mock.Anything, "ctr-1").Return(&domain.Container{
		ID:     "ctr-1",
		Status: string(domain.ContainerStatusRunning),
	}, nil)

	// Running but Docker health says unhealthy.
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "ctr-1").Return("unhealthy", true, nil)

	// Should restart the unhealthy container.
	runtime.EXPECT().RestartContainer(mock.Anything, "ctr-1").Return(nil)

	m := newMonitor(svc)
	m.check(monitorTestContext())

	runtime.AssertExpectations(t)
}

func TestMonitor_CrashLoopBackoff(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := newTestService(runtime)

	svc.containers["app.example.com"] = &domain.Container{
		ID:    "ctr-1",
		Image: "myapp:latest",
	}

	m := newMonitor(svc)
	ctx := monitorTestContext()
	now := time.Now()

	// Simulate 3 crashes within the crash loop window.
	m.mu.Lock()
	m.history["app.example.com"] = &restartRecord{
		attempts:    []time.Time{now.Add(-2 * time.Minute), now.Add(-1 * time.Minute)},
		consecutive: 2,
	}
	m.mu.Unlock()

	// Third crash — this should trigger backoff.
	runtime.EXPECT().InspectContainer(mock.Anything, "ctr-1").Return(&domain.Container{
		ID:       "ctr-1",
		Status:   string(domain.ContainerStatusExited),
		ExitCode: 137,
	}, nil)

	// Should NOT call StartContainer because crash loop is detected.
	m.check(ctx)

	runtime.AssertExpectations(t)
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)

	// Verify backoff was set.
	m.mu.Lock()
	rec := m.history["app.example.com"]
	assert.False(t, rec.backoffEnd.IsZero(), "backoff should be set after crash loop detection")
	assert.True(t, rec.backoffEnd.After(time.Now()), "backoff end should be in the future")
	m.mu.Unlock()
}

func TestMonitor_BackoffPreventsRestart(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := newTestService(runtime)

	svc.containers["app.example.com"] = &domain.Container{
		ID:    "ctr-1",
		Image: "myapp:latest",
	}

	m := newMonitor(svc)

	// Set backoff into the future.
	m.mu.Lock()
	m.history["app.example.com"] = &restartRecord{
		backoffEnd:  time.Now().Add(5 * time.Minute),
		consecutive: 3,
	}
	m.mu.Unlock()

	// Container is crashed.
	runtime.EXPECT().InspectContainer(mock.Anything, "ctr-1").Return(&domain.Container{
		ID:       "ctr-1",
		Status:   string(domain.ContainerStatusExited),
		ExitCode: 1,
	}, nil)

	// Should NOT restart — in backoff.
	m.check(monitorTestContext())

	runtime.AssertExpectations(t)
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
}

func TestMonitor_ClearsCrashHistoryAfterStableRunning(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := newTestService(runtime)

	svc.containers["app.example.com"] = &domain.Container{
		ID:    "ctr-1",
		Image: "myapp:latest",
	}

	m := newMonitor(svc)

	// Container has been running for longer than stableRunningDuration.
	m.mu.Lock()
	m.history["app.example.com"] = &restartRecord{
		attempts:    []time.Time{time.Now().Add(-10 * time.Minute)},
		consecutive: 2,
		lastSeen:    time.Now().Add(-6 * time.Minute), // 6 min ago > stableRunningDuration (5 min)
	}
	m.mu.Unlock()

	runtime.EXPECT().InspectContainer(mock.Anything, "ctr-1").Return(&domain.Container{
		ID:     "ctr-1",
		Status: string(domain.ContainerStatusRunning),
	}, nil)

	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "ctr-1").Return("healthy", true, nil)

	m.check(monitorTestContext())

	// History should be cleared.
	m.mu.Lock()
	_, exists := m.history["app.example.com"]
	m.mu.Unlock()
	assert.False(t, exists, "crash history should be cleared after stable running")

	runtime.AssertExpectations(t)
}

func TestMonitor_SkipsReplacedContainer(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := newTestService(runtime)

	// Track the OLD container ID.
	svc.containers["app.example.com"] = &domain.Container{
		ID:    "ctr-NEW",
		Image: "myapp:latest",
	}

	m := newMonitor(svc)

	// Monitor snapshot captured "ctr-OLD" but a deploy replaced it with "ctr-NEW".
	// We simulate this by directly calling checkContainer with the old ID.
	ctx := monitorTestContext()
	log := zerowrap.FromCtx(ctx)

	// Inspect the old container — it's exited.
	runtime.EXPECT().InspectContainer(mock.Anything, "ctr-OLD").Return(&domain.Container{
		ID:       "ctr-OLD",
		Status:   string(domain.ContainerStatusExited),
		ExitCode: 1,
	}, nil)

	// Should NOT restart — tracked container has been replaced.
	oldTracked := &domain.Container{ID: "ctr-OLD", Image: "myapp:latest"}
	m.checkContainer(ctx, log, "app.example.com", oldTracked, time.Now())

	runtime.AssertExpectations(t)
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
}

func TestMonitor_ComputeBackoff(t *testing.T) {
	m := &Monitor{}

	tests := []struct {
		consecutive int
		expected    time.Duration
	}{
		{3, 1 * time.Minute},   // first backoff
		{4, 2 * time.Minute},   // second
		{5, 4 * time.Minute},   // third
		{6, 8 * time.Minute},   // fourth
		{7, 15 * time.Minute},  // capped
		{10, 15 * time.Minute}, // still capped
	}

	for _, tt := range tests {
		got := m.computeBackoff(tt.consecutive)
		assert.Equal(t, tt.expected, got, "consecutive=%d", tt.consecutive)
	}
}

func TestMonitor_StartStop(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := newTestService(runtime)

	m := newMonitor(svc)
	m.interval = 50 * time.Millisecond // fast ticking for test

	ctx, cancel := context.WithCancel(monitorTestContext())
	defer cancel()

	m.Start(ctx)

	// Let it tick a couple times.
	time.Sleep(150 * time.Millisecond)

	// Stop should return promptly.
	done := make(chan struct{})
	go func() {
		m.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("monitor.Stop() did not return within 2 seconds")
	}
}
