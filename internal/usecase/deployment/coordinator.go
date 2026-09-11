package deployment

import (
	"context"
	"sync"
)

// appCoordinator serializes workload mutations per app. User mutations
// and boot acquire the app lock blocking; periodic reconciliation
// probes it with TryAcquire and skips busy apps instead of waiting.
// Entries are never deleted so a lock cannot be re-created under a
// holder; the map stays bounded by known app names.
type appCoordinator struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

func newAppCoordinator() *appCoordinator {
	return &appCoordinator{locks: map[string]chan struct{}{}}
}

// lockFor returns the app's lock channel, creating it free (token
// present) on first use. The caller must hold mu.
func (c *appCoordinator) lockFor(app string) chan struct{} {
	ch, ok := c.locks[app]
	if !ok {
		ch = make(chan struct{}, 1)
		ch <- struct{}{}
		c.locks[app] = ch
	}
	return ch
}

// acquire blocks until the app lock is held or ctx ends. The returned
// release must be called exactly once by the holder.
func (c *appCoordinator) acquire(ctx context.Context, app string) (func(), error) {
	c.mu.Lock()
	ch := c.lockFor(app)
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-ch:
		return func() { ch <- struct{}{} }, nil
	}
}

// tryAcquire takes the app lock without blocking. ok is false when the
// app is busy; the caller must skip the app in that case.
func (c *appCoordinator) tryAcquire(app string) (release func(), ok bool) {
	c.mu.Lock()
	ch := c.lockFor(app)
	c.mu.Unlock()
	select {
	case <-ch:
		return func() { ch <- struct{}{} }, true
	default:
		return nil, false
	}
}
