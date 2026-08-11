// Package registrystate coordinates registry mutations and garbage collection.
package registrystate

import (
	"sync"
	"time"
)

// PendingBlobTTL protects uploaded blobs while clients publish their manifest.
const PendingBlobTTL = 24 * time.Hour

// State is shared by registry writes and registry garbage collection.
type State struct {
	MutationMu sync.RWMutex

	pendingMu sync.Mutex
	pending   map[string]time.Time
}

// New creates empty registry coordination state.
func New() *State {
	return &State{pending: make(map[string]time.Time)}
}

// AddPending records a blob that has been uploaded but not yet referenced by a manifest.
func (s *State) AddPending(digest string, now time.Time) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if s.pending == nil {
		s.pending = make(map[string]time.Time)
	}
	s.pending[digest] = now
}

// MarkPublished removes blobs now owned by a published manifest from the pending set.
func (s *State) MarkPublished(digests []string) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	for _, digest := range digests {
		delete(s.pending, digest)
	}
}

// PendingDigests returns non-expired pending blobs and drops abandoned entries.
func (s *State) PendingDigests(now time.Time) map[string]struct{} {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	result := make(map[string]struct{}, len(s.pending))
	for digest, createdAt := range s.pending {
		if now.Sub(createdAt) > PendingBlobTTL {
			delete(s.pending, digest)
			continue
		}
		result[digest] = struct{}{}
	}
	return result
}
