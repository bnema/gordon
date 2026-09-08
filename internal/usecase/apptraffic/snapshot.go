package apptraffic

import (
	"fmt"
	"sort"
	"sync"

	"github.com/bnema/gordon/internal/domain"
)

// Snapshot is one coordinated routing snapshot: merged app projections
// plus global external routes, owned by the committing operation.
type Snapshot struct {
	// ID is the snapshot generation. Commits race on it; a stale
	// holder's commit is rejected with ErrSnapshotConflict.
	ID uint64
	// Op is the committing operation id.
	Op string
	// Entries are the merged route entries, stably sorted.
	Entries []RouteEntry
	// External routes carried through from installation config.
	External map[string]string
}

// SnapshotStore holds the current snapshot with compare-and-commit
// semantics. Cross-app concurrent updates never overwrite each other:
// the loser re-projects and retries (bounded by the caller) instead
// of blindly replacing.
type SnapshotStore struct {
	mu      sync.Mutex
	current Snapshot
}

// NewSnapshotStore creates an empty snapshot store.
func NewSnapshotStore() *SnapshotStore {
	return &SnapshotStore{}
}

// Current returns a copy of the committed snapshot.
func (s *SnapshotStore) Current() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSnapshot(s.current)
}

// Commit installs a candidate snapshot when its base matches the current
// generation. On mismatch it returns ErrAppTrafficSnapshotConflict and
// leaves the current snapshot untouched.
func (s *SnapshotStore) Commit(op string, baseID uint64, entries []RouteEntry, external map[string]string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if baseID != s.current.ID {
		return Snapshot{}, fmt.Errorf(
			"%w: op %s based on snapshot %d, current is %d",
			domain.ErrAppTrafficSnapshotConflict, op, baseID, s.current.ID,
		)
	}
	next := Snapshot{
		ID:       s.current.ID + 1,
		Op:       op,
		Entries:  append([]RouteEntry(nil), entries...),
		External: cloneExternal(external),
	}
	sort.Slice(next.Entries, func(i, j int) bool {
		return next.Entries[i].RouterName < next.Entries[j].RouterName
	})
	s.current = next
	return cloneSnapshot(next), nil
}

// Merge builds a candidate snapshot from per-app projections plus global
// external routes. Withdrawing one app's entries preserves all unrelated
// traffic: entries are merged by router name, never bulk-replaced without
// a diff check by the caller.
func Merge(projections map[string][]RouteEntry, external map[string]string) []RouteEntry {
	var merged []RouteEntry
	apps := make([]string, 0, len(projections))
	for app := range projections {
		apps = append(apps, app)
	}
	sort.Strings(apps)
	for _, app := range apps {
		merged = append(merged, projections[app]...)
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].RouterName < merged[j].RouterName
	})
	return merged
}

// DiffSnapshots compares two snapshots by router name, returning the
// withdrawn and added router names. Unrelated entries must be equal;
// callers use this to prove withdrawal preserves other traffic.
func DiffSnapshots(old, new Snapshot) (withdrawn, added []string) {
	oldRouters := map[string]RouteEntry{}
	for _, entry := range old.Entries {
		oldRouters[entry.RouterName] = entry
	}
	newRouters := map[string]RouteEntry{}
	for _, entry := range new.Entries {
		newRouters[entry.RouterName] = entry
	}
	for name, oldEntry := range oldRouters {
		newEntry, ok := newRouters[name]
		if !ok || newEntry != oldEntry {
			withdrawn = append(withdrawn, name)
		}
	}
	for name := range newRouters {
		if _, ok := oldRouters[name]; !ok {
			added = append(added, name)
		}
	}
	sort.Strings(withdrawn)
	sort.Strings(added)
	return withdrawn, added
}

// cloneSnapshot deep-copies a snapshot.
func cloneSnapshot(s Snapshot) Snapshot {
	out := Snapshot{ID: s.ID, Op: s.Op, External: cloneExternal(s.External)}
	out.Entries = append([]RouteEntry(nil), s.Entries...)
	return out
}

// cloneExternal copies the external route map.
func cloneExternal(external map[string]string) map[string]string {
	if external == nil {
		return nil
	}
	out := make(map[string]string, len(external))
	for k, v := range external {
		out[k] = v
	}
	return out
}
