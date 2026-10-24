package apptraffic

import (
	"sync"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// HoldSource supplies live in-memory handover deadlines.
type HoldSource interface {
	Until(app, service string) (time.Time, bool)
}

type holdKey struct{ app, service string }

// Holds tracks process-local handovers. A crash drops holds along with waiting
// clients. An abandoned in-process deploy may leave a hold until its deadline.
type Holds struct {
	mu    sync.Mutex
	until map[holdKey]time.Time
	now   func() time.Time
}

// NewHolds creates an empty registry.
func NewHolds() *Holds { return &Holds{until: make(map[holdKey]time.Time), now: time.Now} }

// Set registers or replaces a service's deadline.
func (h *Holds) Set(app, service string, until time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.until[holdKey{app, service}] = until
}

// Drop removes a service's hold.
func (h *Holds) Drop(app, service string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.until, holdKey{app, service})
}

// Until returns only live holds, lazily deleting expired entries.
func (h *Holds) Until(app, service string) (time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := holdKey{app, service}
	until, ok := h.until[key]
	if !ok || !h.now().Before(until) {
		delete(h.until, key)
		return time.Time{}, false
	}
	return until, true
}

// Prune drops expired, removed, or already-published services' holds so that
// a later plain withdrawal cannot revive a stale handover.
func (h *Holds) Prune(active map[string]domain.AppActive) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	for key, until := range h.until {
		service, ok := active[key.app].Services[key.service]
		if !ok || len(service.BackendBinds) > 0 || len(service.UDPBackendBinds) > 0 || !now.Before(until) {
			delete(h.until, key)
		}
	}
}
