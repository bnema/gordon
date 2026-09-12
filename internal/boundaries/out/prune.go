package out

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// PruneProtectionStore builds the coherent protection snapshot prune
// plans against. The implementation must read every durable fact in one
// transaction so no candidate is judged against facts from a different
// moment, and must report unreadable state as an inventory gap instead
// of returning an empty (apparently unprotected) snapshot.
type PruneProtectionStore interface {
	// ProtectionSnapshot returns every durable fact that protects prune
	// candidates, including desired and active app state, recovery
	// inhibitions, staged/committed apply intents, unfinished
	// operations, and ownership records.
	ProtectionSnapshot(ctx context.Context) (*domain.PruneProtectionSnapshot, error)
}

// PruneRuntime inventories runtime resources and deletes exactly the
// planned targets. It is deliberately not part of ContainerRuntime:
// prune needs enriched inventory and exact, non-force deletion, and no
// other use case may reach an implicit runtime-wide prune.
type PruneRuntime interface {
	// InventoryRuntime returns one coherent read of runtime images,
	// container image usage (running and stopped), and volumes with
	// labels and usage. A failure to inspect part of the runtime is
	// reported as an inventory gap, never as absence.
	InventoryRuntime(ctx context.Context) (*domain.RuntimeInventory, error)

	// RemoveImageExact removes exactly the named runtime image ID with
	// force=false. It never removes images selected by a filter.
	RemoveImageExact(ctx context.Context, ref domain.RuntimeImageRef) error

	// RemoveVolumeExact removes exactly the named runtime volume with
	// force=false. It never removes volumes selected by a filter.
	RemoveVolumeExact(ctx context.Context, ref domain.RuntimeVolumeRef) error
}

// GCBarrier serializes resource acquisition and its durable protection
// publication against destructive garbage collection.
//
// A shared lease must be held from the moment a deploy, start, restart,
// remove, recovery, or restore path selects or acquires a runtime
// resource until that resource's protection is durably published
// (journal step, ACTIVE state, ownership record). An exclusive lease
// covers prune's snapshot read, planning, and deletion.
//
// Lock order is fixed and documented so the two never deadlock:
// GC barrier → per-app coordinator → registry mutation lock → short
// bbolt transaction.
type GCBarrier interface {
	// AcquireShared blocks until no exclusive lease is held, then
	// returns a lease the caller must release. It returns ctx.Err()
	// when ctx is done or when shutdown happens first.
	AcquireShared(ctx context.Context) (GCLease, error)
	// AcquireExclusive blocks until no lease of either kind is held,
	// then returns a lease the caller must release. It returns ctx.Err()
	// when ctx is done or when shutdown happens first.
	AcquireExclusive(ctx context.Context) (GCLease, error)
}

// GCLease is one held GC barrier lease. Release is idempotent; callers
// should still defer exactly one Release per acquired lease.
type GCLease interface {
	Release()
}
