package container

import (
	"context"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

const (
	monitorDefaultInterval = 15 * time.Second
	crashLoopThreshold     = 3
	crashLoopWindow        = 5 * time.Minute
	backoffCap             = 15 * time.Minute
	stableRunningDuration  = 5 * time.Minute
)

// restartRecord tracks restart attempts for crash loop detection.
type restartRecord struct {
	attempts    []time.Time
	backoffEnd  time.Time
	consecutive int
	lastSeen    time.Time // last time the container was seen running
}

// Monitor watches tracked containers and restarts crashed ones.
type Monitor struct {
	service  *Service
	stopCh   chan struct{}
	stopped  chan struct{}
	interval time.Duration
	mu       sync.Mutex
	history  map[string]*restartRecord // keyed by domain
}

// newMonitor creates a new container monitor.
func newMonitor(service *Service) *Monitor {
	return &Monitor{
		service:  service,
		stopCh:   make(chan struct{}),
		stopped:  make(chan struct{}),
		interval: monitorDefaultInterval,
		history:  make(map[string]*restartRecord),
	}
}

// Start begins the background monitoring loop.
func (m *Monitor) Start(ctx context.Context) {
	log := zerowrap.FromCtx(ctx)
	log.Info().Dur("interval", m.interval).Msg("container monitor started")

	go m.run(ctx)
}

// Stop signals the monitor to stop and waits for it to finish.
func (m *Monitor) Stop() {
	close(m.stopCh)
	<-m.stopped
}

func (m *Monitor) run(ctx context.Context) {
	defer close(m.stopped)

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.check(ctx)
		}
	}
}

func (m *Monitor) check(ctx context.Context) {
	log := zerowrap.FromCtx(ctx)

	// Snapshot tracked containers under lock.
	m.service.mu.RLock()
	snapshot := make(map[string]*domain.Container, len(m.service.containers))
	for d, c := range m.service.containers {
		snapshot[d] = c
	}
	m.service.mu.RUnlock()

	now := time.Now()

	for domainName, tracked := range snapshot {
		m.checkContainer(ctx, log, domainName, tracked, now)
	}
}

func (m *Monitor) checkContainer(ctx context.Context, log zerowrap.Logger, domainName string, tracked *domain.Container, now time.Time) {
	if tracked == nil {
		return
	}

	inspected, err := m.service.runtime.InspectContainer(ctx, tracked.ID)
	if err != nil {
		log.Debug().Err(err).Str("domain", domainName).Str("container_id", tracked.ID).
			Msg("monitor: failed to inspect container, may have been replaced by deploy")
		return
	}

	switch {
	case inspected.Status == string(domain.ContainerStatusRunning):
		m.handleRunning(ctx, log, domainName, tracked.ID, now)

	case inspected.Status == string(domain.ContainerStatusExited):
		if inspected.ExitCode == 0 {
			// Graceful stop (exit 0): respect user intent, don't restart.
			log.Debug().Str("domain", domainName).Msg("monitor: container exited gracefully (code 0), skipping")
			return
		}
		// Crashed (non-zero exit): restart if not in backoff.
		m.handleCrash(ctx, log, domainName, tracked.ID, now)
	}
}

func (m *Monitor) handleRunning(ctx context.Context, log zerowrap.Logger, domainName, containerID string, now time.Time) {
	// Check Docker health status for running containers.
	healthStatus, hasHealthcheck, err := m.service.runtime.GetContainerHealthStatus(ctx, containerID)
	if err != nil {
		return
	}

	if hasHealthcheck && healthStatus == "unhealthy" {
		log.Warn().Str("domain", domainName).Msg("monitor: container is unhealthy, restarting")
		if err := m.service.runtime.RestartContainer(ctx, containerID); err != nil {
			log.Warn().Err(err).Str("domain", domainName).Msg("monitor: failed to restart unhealthy container")
		}
		return
	}

	// Container is running and healthy — clear backoff if stable long enough.
	m.mu.Lock()
	rec, exists := m.history[domainName]
	if exists {
		if rec.lastSeen.IsZero() {
			rec.lastSeen = now
		}
		if now.Sub(rec.lastSeen) >= stableRunningDuration {
			// Container has been running for 5+ min — clear crash history.
			delete(m.history, domainName)
			m.mu.Unlock()
			log.Info().Str("domain", domainName).Msg("monitor: container stable, cleared crash history")
			return
		}
	}
	m.mu.Unlock()
}

func (m *Monitor) handleCrash(ctx context.Context, log zerowrap.Logger, domainName, containerID string, now time.Time) {
	m.mu.Lock()
	rec := m.history[domainName]
	if rec == nil {
		rec = &restartRecord{}
		m.history[domainName] = rec
	}

	// Check backoff.
	if now.Before(rec.backoffEnd) {
		m.mu.Unlock()
		log.Debug().Str("domain", domainName).
			Time("backoff_until", rec.backoffEnd).
			Msg("monitor: in crash loop backoff, skipping restart")
		return
	}

	// Record this crash attempt.
	rec.attempts = append(rec.attempts, now)
	rec.consecutive++
	rec.lastSeen = time.Time{} // reset stable timer

	// Prune old attempts outside the crash loop window.
	cutoff := now.Add(-crashLoopWindow)
	pruned := rec.attempts[:0]
	for _, t := range rec.attempts {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	rec.attempts = pruned

	// Check for crash loop.
	if len(rec.attempts) >= crashLoopThreshold {
		backoff := m.computeBackoff(rec.consecutive)
		rec.backoffEnd = now.Add(backoff)
		m.mu.Unlock()

		log.Error().Str("domain", domainName).
			Int("consecutive", rec.consecutive).
			Dur("backoff", backoff).
			Msg("monitor: crash loop detected, backing off")
		return
	}
	m.mu.Unlock()

	// Verify container ID still matches tracked state (deploy may have replaced it).
	m.service.mu.RLock()
	current := m.service.containers[domainName]
	m.service.mu.RUnlock()
	if current == nil || current.ID != containerID {
		log.Debug().Str("domain", domainName).Msg("monitor: container replaced by deploy, skipping restart")
		return
	}

	log.Warn().Str("domain", domainName).Str("container_id", containerID).
		Int("exit_code", -1). // exact code already logged by inspect
		Msg("monitor: restarting crashed container")

	if err := m.service.runtime.StartContainer(ctx, containerID); err != nil {
		log.Warn().Err(err).Str("domain", domainName).Msg("monitor: failed to restart container")
	}
}

func (m *Monitor) computeBackoff(consecutive int) time.Duration {
	// Exponential backoff: 1min, 2min, 4min, 8min, cap 15min.
	base := time.Minute
	shift := consecutive - crashLoopThreshold
	if shift < 0 {
		shift = 0
	}
	if shift > 4 {
		shift = 4 // cap shift to avoid overflow (max 16min, then capped to backoffCap)
	}
	backoff := base << uint(shift) // #nosec G115 -- shift is bounded [0,4]
	if backoff > backoffCap {
		backoff = backoffCap
	}
	return backoff
}
