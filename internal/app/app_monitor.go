package app

import (
	"context"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
)

// appMonitorInterval is the fixed periodic recovery cadence.
const appMonitorInterval = 15 * time.Second

// appMonitor is the daemon-owned driver of periodic app recovery. One
// instance lives for the whole process: the loop is created once after
// boot reconciliation and reload never starts another. Passes never
// overlap because a single goroutine runs them.
type appMonitor struct {
	reconciler in.AppReconciler
	interval   time.Duration
	// ticks lets tests drive the cadence deterministically. Nil uses a
	// real ticker.
	ticks func() <-chan time.Time
	log   zerowrap.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newAppMonitor(reconciler in.AppReconciler, log zerowrap.Logger) *appMonitor {
	return &appMonitor{reconciler: reconciler, interval: appMonitorInterval, log: log}
}

// Start launches the recovery loop under the daemon context. It is
// idempotent: a running monitor is left untouched.
func (m *appMonitor) Start(ctx context.Context) {
	if m == nil || m.reconciler == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	done := make(chan struct{})
	m.done = done
	go m.loop(runCtx, done)
}

// Stop cancels the loop and joins it, so shutdown never races runtime,
// traffic, or state teardown. Safe to call more than once.
func (m *appMonitor) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.cancel, m.done = nil, nil
	m.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

func (m *appMonitor) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	var ticks <-chan time.Time
	if m.ticks != nil {
		ticks = m.ticks()
	} else {
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if err := m.reconciler.ReconcileRunning(ctx); err != nil {
				m.log.Warn().Err(err).Msg("app reconciliation pass failed")
			}
		}
	}
}
