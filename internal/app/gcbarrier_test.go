package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
)

func TestGCBarrierExcludesExclusiveDuringShared(t *testing.T) {
	barrier := newGCBarrier()
	ctx := context.Background()

	shared, err := barrier.AcquireShared(ctx)
	if err != nil {
		t.Fatalf("AcquireShared: %v", err)
	}

	// An exclusive lease must wait until the last shared holder is
	// done, so a prune cannot plan against a half-acquired resource.
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := barrier.AcquireExclusive(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exclusive lease during a shared lease returned %v, want deadline exceeded", err)
	}

	exclusive, err := acquireExclusiveAfter(barrier, shared)
	if err != nil {
		t.Fatalf("exclusive lease after release: %v", err)
	}
	exclusive.Release()
}

// acquireExclusiveAfter releases shared, then takes the exclusive lease
// that must now be grantable.
func acquireExclusiveAfter(barrier *gcBarrier, shared out.GCLease) (out.GCLease, error) {
	shared.Release()
	return barrier.AcquireExclusive(context.Background())
}

func TestGCBarrierExcludesSharedDuringExclusive(t *testing.T) {
	barrier := newGCBarrier()
	ctx := context.Background()

	exclusive, err := barrier.AcquireExclusive(ctx)
	if err != nil {
		t.Fatalf("AcquireExclusive: %v", err)
	}
	defer exclusive.Release()

	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := barrier.AcquireShared(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shared lease during an exclusive lease returned %v, want deadline exceeded", err)
	}

	// A second exclusive lease also waits.
	if _, err := barrier.AcquireExclusive(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second exclusive lease returned %v, want deadline exceeded", err)
	}

	exclusive.Release()
	shared, err := barrier.AcquireShared(ctx)
	if err != nil {
		t.Fatalf("shared lease after release: %v", err)
	}
	shared.Release()
}

func TestGCBarrierCancellation(t *testing.T) {
	barrier := newGCBarrier()
	held, err := barrier.AcquireExclusive(context.Background())
	if err != nil {
		t.Fatalf("AcquireExclusive: %v", err)
	}
	defer held.Release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := barrier.AcquireShared(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquisition returned %v, want context.Canceled", err)
	}

	// A waiter canceled while blocked returns instead of wedging
	// shutdown.
	waitCtx, cancelWait := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := barrier.AcquireShared(waitCtx)
		done <- err
	}()
	cancelWait()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked waiter returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled waiter did not return")
	}
}

func TestGCBarrierDeterministicHandoff(t *testing.T) {
	barrier := newGCBarrier()
	ctx := context.Background()

	// Simulate a workload mutation holding the lease across resource
	// acquisition and durable publication.
	shared, err := barrier.AcquireShared(ctx)
	if err != nil {
		t.Fatalf("AcquireShared: %v", err)
	}

	acquired := make(chan struct{})
	releaseExclusive := make(chan struct{})
	go func() {
		exclusive, err := barrier.AcquireExclusive(ctx)
		if err != nil {
			close(acquired)
			return
		}
		close(acquired)
		<-releaseExclusive
		exclusive.Release()
	}()

	// The exclusive lease must not be granted while the shared lease is
	// held, no matter how the goroutine is scheduled.
	select {
	case <-acquired:
		t.Fatal("exclusive lease granted while a shared lease was held")
	case <-time.After(50 * time.Millisecond):
	}

	shared.Release()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("exclusive lease was never granted after the shared lease was released")
	}

	// Once the exclusive lease is held, a new shared lease waits.
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := barrier.AcquireShared(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shared lease during handoff returned %v, want deadline exceeded", err)
	}

	close(releaseExclusive)
	// The barrier must drain back to idle.
	deadline := time.Now().Add(2 * time.Second)
	for {
		lease, err := barrier.AcquireExclusive(context.Background())
		if err == nil {
			lease.Release()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("barrier never returned to idle")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGCBarrierConcurrentSharedHolders(t *testing.T) {
	barrier := newGCBarrier()
	ctx := context.Background()

	const holders = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range holders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lease, err := barrier.AcquireShared(ctx)
			if err != nil {
				t.Errorf("AcquireShared: %v", err)
				return
			}
			time.Sleep(time.Millisecond)
			lease.Release()
		}()
	}
	close(start)

	// While any shared holder may be active, an exclusive lease must
	// never be granted concurrently with one.
	exclusiveDone := make(chan struct{})
	go func() {
		defer close(exclusiveDone)
		for range 200 {
			lease, err := barrier.AcquireExclusive(ctx)
			if err != nil {
				t.Errorf("AcquireExclusive: %v", err)
				return
			}
			barrier.mu.Lock()
			shared := barrier.shared
			holding := barrier.exclusive
			barrier.mu.Unlock()
			if !holding || shared != 0 {
				t.Errorf("exclusive lease held with %d shared holders (exclusive=%v)", shared, holding)
			}
			lease.Release()
		}
	}()

	wg.Wait()
	<-exclusiveDone
}

func TestGCBarrierLeaseReleaseIsIdempotent(t *testing.T) {
	barrier := newGCBarrier()
	lease, err := barrier.AcquireShared(context.Background())
	if err != nil {
		t.Fatalf("AcquireShared: %v", err)
	}
	lease.Release()
	lease.Release()

	barrier.mu.Lock()
	shared := barrier.shared
	barrier.mu.Unlock()
	if shared != 0 {
		t.Fatalf("shared count after double release = %d, want 0", shared)
	}

	exclusive, err := barrier.AcquireExclusive(context.Background())
	if err != nil {
		t.Fatalf("AcquireExclusive after double release: %v", err)
	}
	exclusive.Release()
}
