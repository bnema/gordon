package app

import (
	"context"
	"sync"

	"github.com/bnema/gordon/internal/boundaries/out"
)

// gcBarrier is the process-wide GC barrier.
//
// Shared leases cover the window in which a deploy, start, restart,
// remove, recovery, or restore path selects, acquires, and durably
// publishes the protection of a runtime resource. One exclusive lease
// covers prune's protection snapshot, planning, and exact deletion.
//
// Lock order is fixed: GC barrier → per-app coordinator → registry
// mutation lock → short bbolt transaction. Callers therefore take a
// shared lease BEFORE the per-app coordinator, never the other way
// round.
type gcBarrier struct {
	mu        sync.Mutex
	shared    int
	exclusive bool
	// changed is closed and replaced whenever a lease is released, so
	// waiters re-check availability.
	changed chan struct{}
}

// newGCBarrier creates an idle process-wide barrier.
func newGCBarrier() *gcBarrier {
	return &gcBarrier{changed: make(chan struct{})}
}

// AcquireShared implements out.GCBarrier.
func (b *gcBarrier) AcquireShared(ctx context.Context) (out.GCLease, error) {
	return b.acquire(ctx, false)
}

// AcquireExclusive implements out.GCBarrier.
func (b *gcBarrier) AcquireExclusive(ctx context.Context) (out.GCLease, error) {
	return b.acquire(ctx, true)
}

func (b *gcBarrier) acquire(ctx context.Context, exclusive bool) (out.GCLease, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b.mu.Lock()
		if b.available(exclusive) {
			if exclusive {
				b.exclusive = true
			} else {
				b.shared++
			}
			b.mu.Unlock()
			return &gcLease{barrier: b, exclusive: exclusive}, nil
		}
		wait := b.changed
		b.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait:
		}
	}
}

// available reports whether a lease of the requested kind can be taken
// without violating the exclusion rules. The caller holds mu.
func (b *gcBarrier) available(exclusive bool) bool {
	if exclusive {
		return !b.exclusive && b.shared == 0
	}
	return !b.exclusive
}

// release drops one held lease and wakes waiters. The caller must hold
// no lock.
func (b *gcBarrier) release(exclusive bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if exclusive {
		b.exclusive = false
	} else if b.shared > 0 {
		b.shared--
	}
	close(b.changed)
	b.changed = make(chan struct{})
}

// gcLease is one held barrier lease. Release is idempotent.
type gcLease struct {
	barrier   *gcBarrier
	exclusive bool
	once      sync.Once
}

// Release implements out.GCLease.
func (l *gcLease) Release() {
	if l == nil || l.barrier == nil {
		return
	}
	l.once.Do(func() { l.barrier.release(l.exclusive) })
}

var _ out.GCBarrier = (*gcBarrier)(nil)
