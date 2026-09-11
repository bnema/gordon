package deployment

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestAppCoordinator_SerializesOneApp(t *testing.T) {
	coord := newAppCoordinator()
	release, err := coord.acquire(context.Background(), "blog")
	require.NoError(t, err)

	acquired := make(chan struct{})
	go func() {
		release2, err := coord.acquire(context.Background(), "blog")
		if err == nil {
			close(acquired)
			release2()
		}
	}()

	select {
	case <-acquired:
		t.Fatal("second acquisition ran while the app lock was held")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second acquisition never ran after release")
	}
}

func TestAppCoordinator_AllowsParallelApps(t *testing.T) {
	coord := newAppCoordinator()
	releaseBlog, err := coord.acquire(context.Background(), "blog")
	require.NoError(t, err)
	defer releaseBlog()

	releaseWiki, err := coord.acquire(context.Background(), "wiki")
	require.NoError(t, err)
	defer releaseWiki()

	// Distinct apps must not block each other.
	_, ok := coord.tryAcquire("other")
	assert.True(t, ok)
}

func TestAppCoordinator_TryAcquireSkipsBusyApp(t *testing.T) {
	coord := newAppCoordinator()
	release, ok := coord.tryAcquire("blog")
	require.True(t, ok)

	_, ok = coord.tryAcquire("blog")
	assert.False(t, ok, "a periodic pass must skip a busy app instead of waiting")

	release()
	_, ok = coord.tryAcquire("blog")
	assert.True(t, ok, "the lock is reusable after release")
}

func TestAppCoordinator_HonorsCancellation(t *testing.T) {
	coord := newAppCoordinator()
	release, ok := coord.tryAcquire("blog")
	require.True(t, ok)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := coord.acquire(ctx, "blog")
	require.ErrorIs(t, err, context.Canceled)
	release()
}

func TestAppCoordinator_ConcurrentUseIsRaceFree(t *testing.T) {
	coord := newAppCoordinator()
	var wg sync.WaitGroup
	var counter int64
	var mu sync.Mutex
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				release, err := coord.acquire(context.Background(), "blog")
				if err != nil {
					return
				}
				mu.Lock()
				counter++
				mu.Unlock()
				release()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int64(400), counter)
}

func TestRecoveryBackoff_ThreeAttemptsThenEscalatingDelays(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	backoff := newRecoveryBackoff(func() time.Time { return now })
	key := recoveryKey{app: "blog", service: "web", container: "c-1"}

	for attempt := 1; attempt <= 3; attempt++ {
		assert.True(t, backoff.allow(key), "attempt %d must be permitted", attempt)
		backoff.recordFailure(key)
	}
	assert.False(t, backoff.allow(key), "the fourth attempt is delayed by one minute")

	expected := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute}
	for i, delay := range expected {
		now = now.Add(delay)
		assert.True(t, backoff.allow(key), "delay %d must expire", i)
		backoff.recordFailure(key)
		assert.False(t, backoff.allow(key))
	}
	// Further failures stay capped at fifteen minutes.
	now = now.Add(15 * time.Minute)
	assert.True(t, backoff.allow(key))
	backoff.recordFailure(key)
	now = now.Add(14 * time.Minute)
	assert.False(t, backoff.allow(key), "the cap holds at fifteen minutes")
	now = now.Add(time.Minute)
	assert.True(t, backoff.allow(key))
}

func TestRecoveryBackoff_EscalationSurvivesWithoutStableObservation(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	backoff := newRecoveryBackoff(func() time.Time { return now })
	key := recoveryKey{app: "blog", service: "web", container: "c-1"}

	for range 3 {
		backoff.recordFailure(key)
	}
	require.False(t, backoff.allow(key))

	// A long idle period permits the delayed attempt, but the escalation
	// itself only resets after five continuously stable minutes.
	now = now.Add(30 * time.Minute)
	require.True(t, backoff.allow(key))
	backoff.recordFailure(key)
	assert.False(t, backoff.allow(key), "the failure budget is not escaped by waiting")
}

func TestRecoveryBackoff_StableObservationResetsHistory(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	backoff := newRecoveryBackoff(func() time.Time { return now })
	key := recoveryKey{app: "blog", service: "web", container: "c-1"}

	for range 3 {
		backoff.recordFailure(key)
	}
	require.False(t, backoff.allow(key))

	now = now.Add(30 * time.Second)
	backoff.observeStable(key)
	assert.Equal(t, 3, recoveredFailures(backoff, key), "one healthy observation is not enough")

	now = now.Add(stableReadyWindow - time.Second)
	backoff.observeStable(key)
	assert.Equal(t, 3, recoveredFailures(backoff, key), "history resets only after five stable minutes")

	now = now.Add(2 * time.Second)
	backoff.observeStable(key)
	assert.Equal(t, 0, recoveredFailures(backoff, key), "five stable minutes reset the failure history")
	assert.True(t, backoff.allow(key))
}

// recoveredFailures reports the escalation counter of one generation; 0
// means the history was dropped.
func recoveredFailures(backoff *recoveryBackoff, key recoveryKey) int {
	backoff.mu.Lock()
	defer backoff.mu.Unlock()
	entry, ok := backoff.entries[key]
	if !ok {
		return 0
	}
	return entry.failures
}

func TestRecoveryBackoff_FailureClearsStability(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	backoff := newRecoveryBackoff(func() time.Time { return now })
	key := recoveryKey{app: "blog", service: "web", container: "c-1"}

	backoff.observeStable(key)
	now = now.Add(4 * time.Minute)
	backoff.recordFailure(key)
	now = now.Add(2 * time.Minute)
	backoff.observeStable(key)
	assert.True(t, backoff.allow(key), "the streak restarted at the failure")
}

func TestRecoveryBackoff_IsGenerationScoped(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	backoff := newRecoveryBackoff(func() time.Time { return now })
	failing := recoveryKey{app: "blog", service: "web", container: "c-1"}
	for range 4 {
		backoff.recordFailure(failing)
	}
	assert.False(t, backoff.allow(failing))
	assert.True(t, backoff.allow(recoveryKey{app: "blog", service: "web", container: "c-2"}),
		"a replacement generation starts with a fresh budget")
	assert.True(t, backoff.allow(recoveryKey{app: "blog", service: "worker", container: "c-1"}))
	assert.True(t, backoff.allow(recoveryKey{app: "wiki", service: "web", container: "c-1"}))
}

func TestRecoveryBackoff_ForgetDropsHistory(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	backoff := newRecoveryBackoff(func() time.Time { return now })
	key := recoveryKey{app: "blog", service: "web", container: "c-1"}
	for range 4 {
		backoff.recordFailure(key)
	}
	require.False(t, backoff.allow(key))
	backoff.forget(key)
	assert.True(t, backoff.allow(key))
}

func TestRecoveryBackoff_UnhealthyStreak(t *testing.T) {
	backoff := newRecoveryBackoff(time.Now)
	key := recoveryKey{app: "blog", service: "web", container: "c-1"}

	assert.Equal(t, 1, backoff.observeUnhealthy(key))
	assert.Equal(t, 2, backoff.observeUnhealthy(key))
	backoff.clearUnhealthy(key)
	assert.Equal(t, 1, backoff.observeUnhealthy(key), "a non-unhealthy observation resets the streak")
}

func TestPublicationInhibition_MarkAndClear(t *testing.T) {
	var inhibition publicationInhibition
	assert.False(t, inhibition.inhibited("blog", "web"))
	inhibition.mark("blog", "web")
	assert.True(t, inhibition.inhibited("blog", "web"))
	assert.False(t, inhibition.inhibited("blog", "worker"))
	assert.False(t, inhibition.inhibited("wiki", "web"))
	inhibition.clear("blog", "web")
	assert.False(t, inhibition.inhibited("blog", "web"))
	inhibition.clear("blog", "web") // clearing twice is harmless
}

func TestExecutionTracker_DetectsNewExecution(t *testing.T) {
	var tracker executionTracker
	key := recoveryKey{app: "blog", service: "web", container: "c-1"}
	first := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	assert.True(t, tracker.isNewExecution(key, first),
		"an unseen execution must be verified because the runtime may have restarted it while Gordon was absent")
	tracker.record(key, first)
	assert.False(t, tracker.isNewExecution(key, first), "a stable execution is not new")
	assert.True(t, tracker.isNewExecution(key, first.Add(time.Minute)), "a native restart is a new execution")
	tracker.forget(key)
	assert.True(t, tracker.isNewExecution(key, first))
}

func TestRecoveryBackoff_PendingAttemptIsCharged(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	backoff := newRecoveryBackoff(func() time.Time { return now })
	key := recoveryKey{app: "blog", service: "web", container: "c-1"}

	assert.False(t, backoff.attemptPending(key))
	backoff.markAttempt(key)
	assert.True(t, backoff.attemptPending(key))
	backoff.observeStable(key)
	assert.True(t, backoff.attemptPending(key),
		"one stable observation does not confirm an intervention: only five stable minutes do")
	backoff.observeStable(key)
	assert.True(t, backoff.attemptPending(key))
	now = now.Add(stableReadyWindow + time.Second)
	backoff.now = func() time.Time { return now }
	backoff.observeStable(key)
	assert.False(t, backoff.attemptPending(key), "the stability window confirms and resets the generation")
}

func TestRecoveryBackoff_UnstableObservationInvalidatesTheWindow(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	backoff := newRecoveryBackoff(func() time.Time { return now })
	key := recoveryKey{app: "blog", service: "web", container: "c-1"}

	for range 3 {
		backoff.recordFailure(key)
	}
	now = now.Add(time.Minute)
	backoff.observeStable(key)
	now = now.Add(4 * time.Minute)
	backoff.invalidateStable(key)
	now = now.Add(time.Minute)
	backoff.observeStable(key)
	assert.Equal(t, 3, recoveredFailures(backoff, key),
		"an unhealthy or unverifiable observation restarts the five-minute window")
}

func TestRecoveryBackoff_StableObservationNeverCreatesHistory(t *testing.T) {
	backoff := newRecoveryBackoff(time.Now)
	key := recoveryKey{app: "blog", service: "web", container: "c-1"}
	backoff.observeStable(key)
	assert.Equal(t, 0, recoveredFailures(backoff, key))
	backoff.mu.Lock()
	_, tracked := backoff.entries[key]
	backoff.mu.Unlock()
	assert.False(t, tracked, "healthy services must not accumulate monitor state")
}

func TestPublicationInhibition_RetainsActiveServices(t *testing.T) {
	var inhibition publicationInhibition
	inhibition.mark("blog", "web")
	inhibition.mark("blog", "worker")
	inhibition.mark("wiki", "web")
	assert.Equal(t, []string{"web", "worker"}, inhibition.pending("blog"))
	assert.Equal(t, []string{"web"}, inhibition.pending("wiki"))

	inhibition.retainAppServices("blog", map[string]struct{}{"web": {}})

	assert.Equal(t, []string{"web"}, inhibition.pending("blog"),
		"an active service remains inhibited until withdrawal succeeds")
	assert.Equal(t, []string{"web"}, inhibition.pending("wiki"))
}

// TestWaitLogReady_RejectsAnUnknownExecutionBoundary proves the probe
// fails closed when the runtime reports no execution start: without a
// boundary it would scan the container's whole history and could match a
// marker from a previous execution.
func TestWaitLogReady_RejectsAnUnknownExecutionBoundary(t *testing.T) {
	deps := ProbeDeps{
		containerStart: func(context.Context, string) (time.Time, error) { return time.Time{}, nil },
		logStream: func(context.Context, string, time.Time) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("ready\n")), nil
		},
	}
	spec := domain.AppService{Readiness: domain.AppReadiness{Type: domain.AppReadinessLog, Contains: "ready"}}
	err := waitLogReadyWithDeps(context.Background(), deps, "c-1", spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runtime reported none")
}

// TestWaitLogReady_UsesTheExactContainerAndExecution proves the probe
// reads the passed container ID and the current execution boundary.
func TestWaitLogReady_UsesTheExactContainerAndExecution(t *testing.T) {
	startedAt := time.Date(2026, 9, 10, 12, 0, 0, 123456789, time.UTC)
	var gotID string
	var gotSince time.Time
	deps := ProbeDeps{
		containerStart: func(context.Context, string) (time.Time, error) { return startedAt, nil },
		logStream: func(_ context.Context, containerID string, since time.Time) (io.ReadCloser, error) {
			gotID, gotSince = containerID, since
			return io.NopCloser(strings.NewReader("2026-09-10T12:00:01Z ready\n")), nil
		},
	}
	spec := domain.AppService{Readiness: domain.AppReadiness{Type: domain.AppReadinessLog, Contains: "ready"}}
	require.NoError(t, waitLogReadyWithDeps(context.Background(), deps, "c-exact", spec))
	assert.Equal(t, "c-exact", gotID)
	assert.Equal(t, startedAt, gotSince)
}

// TestWaitLogReady_StreamFailureIsActionable proves a transport failure
// is wrapped with the container it belongs to.
func TestWaitLogReady_StreamFailureIsActionable(t *testing.T) {
	deps := ProbeDeps{
		containerStart: func(context.Context, string) (time.Time, error) { return time.Now(), nil },
		logStream: func(context.Context, string, time.Time) (io.ReadCloser, error) {
			return nil, assert.AnError
		},
	}
	spec := domain.AppService{Readiness: domain.AppReadiness{Type: domain.AppReadinessLog, Contains: "ready"}}
	err := waitLogReadyWithDeps(context.Background(), deps, "c-1", spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "log readiness stream for c-1")
	assert.ErrorIs(t, err, assert.AnError)
}
