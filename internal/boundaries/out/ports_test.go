package out_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/boundaries/out/mocks"
)

// The generated mocks must always satisfy their boundary ports.
var (
	_ out.PruneProtectionStore = (*mocks.MockPruneProtectionStore)(nil)
	_ out.PruneRuntime         = (*mocks.MockPruneRuntime)(nil)
	_ out.GCBarrier            = (*mocks.MockGCBarrier)(nil)
)

// referenceBarrier is the minimal shape any GCBarrier implementation
// must have: shared leases exclude exclusive leases, exclusive leases
// exclude everything, and a blocked acquisition honors context
// cancellation instead of wedging shutdown.
type referenceBarrier struct {
	mu        sync.Mutex
	shared    int
	exclusive bool
	// released is closed whenever a lease is released so waiters retry.
	released chan struct{}
}

func newReferenceBarrier() *referenceBarrier {
	return &referenceBarrier{released: make(chan struct{})}
}

type referenceLease struct {
	barrier   *referenceBarrier
	exclusive bool
	once      sync.Once
}

func (l *referenceLease) Release() {
	l.once.Do(func() {
		l.barrier.mu.Lock()
		if l.exclusive {
			l.barrier.exclusive = false
		} else {
			l.barrier.shared--
		}
		close(l.barrier.released)
		l.barrier.released = make(chan struct{})
		l.barrier.mu.Unlock()
	})
}

func (b *referenceBarrier) acquire(ctx context.Context, exclusive bool) (out.GCLease, error) {
	for {
		b.mu.Lock()
		if b.canAcquire(exclusive) {
			if exclusive {
				b.exclusive = true
			} else {
				b.shared++
			}
			b.mu.Unlock()
			return &referenceLease{barrier: b, exclusive: exclusive}, nil
		}
		wait := b.released
		b.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait:
		}
	}
}

func (b *referenceBarrier) canAcquire(exclusive bool) bool {
	if exclusive {
		return !b.exclusive && b.shared == 0
	}
	return !b.exclusive
}

func (b *referenceBarrier) AcquireShared(ctx context.Context) (out.GCLease, error) {
	return b.acquire(ctx, false)
}

func (b *referenceBarrier) AcquireExclusive(ctx context.Context) (out.GCLease, error) {
	return b.acquire(ctx, true)
}

// releaseAll exercises the contract through the interface, so a change
// to the port breaks this test rather than silently changing semantics.
func acquireShared(t *testing.T, barrier out.GCBarrier, ctx context.Context) out.GCLease {
	t.Helper()
	lease, err := barrier.AcquireShared(ctx)
	if err != nil {
		t.Fatalf("AcquireShared: %v", err)
	}
	return lease
}

func acquireExclusive(t *testing.T, barrier out.GCBarrier, ctx context.Context) out.GCLease {
	t.Helper()
	lease, err := barrier.AcquireExclusive(ctx)
	if err != nil {
		t.Fatalf("AcquireExclusive: %v", err)
	}
	return lease
}

func TestGCBarrierContractExcludesExclusiveDuringShared(t *testing.T) {
	barrier := newReferenceBarrier()
	ctx := context.Background()

	shared := acquireShared(t, barrier, ctx)
	defer shared.Release()

	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := barrier.AcquireExclusive(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exclusive acquisition during a shared lease returned %v, want deadline exceeded", err)
	}

	shared.Release()
	exclusive := acquireExclusive(t, barrier, ctx)
	exclusive.Release()

	// Release is idempotent: a double release must not corrupt counts.
	shared.Release()
	again := acquireShared(t, barrier, ctx)
	again.Release()
}

func TestGCBarrierContractExcludesSharedDuringExclusive(t *testing.T) {
	barrier := newReferenceBarrier()
	ctx := context.Background()

	exclusive := acquireExclusive(t, barrier, ctx)
	defer exclusive.Release()

	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := barrier.AcquireShared(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shared acquisition during an exclusive lease returned %v, want deadline exceeded", err)
	}

	exclusive.Release()
	shared := acquireShared(t, barrier, ctx)
	shared.Release()
}

func TestGCBarrierContractPreCanceledContext(t *testing.T) {
	barrier := newReferenceBarrier()
	held := acquireExclusive(t, barrier, context.Background())
	defer held.Release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := barrier.AcquireShared(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquisition returned %v, want context.Canceled", err)
	}
}
