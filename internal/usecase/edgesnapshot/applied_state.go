package edgesnapshot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const maxAppliedStateEntries = 1024

var (
	ErrAppliedStateUnexpected = errors.New("unexpected edge applied state")
	ErrAppliedStateStale      = errors.New("stale edge applied state")
	ErrAppliedStateInvalid    = errors.New("invalid edge applied state")
)

// AppliedState is the complete, deliberately topology-free edge cutover fact.
// The component name is taken from authenticated transport identity, never
// trusted from an unauthenticated caller.
type AppliedState struct {
	ComponentID       string
	RouteGeneration   uint64
	TrafficGeneration uint64
	Healthy           bool
	ObservedAt        time.Time
}

type AppliedStateReceiver interface {
	ReportAuthenticatedAppliedState(context.Context, string, AppliedState) error
}

// AppliedStateTracker retains a bounded newest report for each expected edge.
// Generations cannot regress, and a report is usable only when both streams
// were applied at the exact same non-zero generation and the edge says healthy.
type AppliedStateTracker struct {
	mu       sync.RWMutex
	expected string
	now      func() time.Time
	states   map[string]AppliedState
	order    []string
}

func NewAppliedStateTracker(expectedComponentID string) (*AppliedStateTracker, error) {
	if strings.TrimSpace(expectedComponentID) == "" {
		return nil, fmt.Errorf("expected edge component ID is required")
	}
	return newAppliedStateTracker(expectedComponentID), nil
}

// NewAppliedStateTrackerAny accepts any authenticated edge identity. The
// switcher must use AppliedFor with the checkpoint's generated edge ID.
func NewAppliedStateTrackerAny() *AppliedStateTracker { return newAppliedStateTracker("") }

func newAppliedStateTracker(expectedComponentID string) *AppliedStateTracker {
	return &AppliedStateTracker{expected: expectedComponentID, now: time.Now, states: make(map[string]AppliedState)}
}

func (t *AppliedStateTracker) ReportAuthenticatedAppliedState(ctx context.Context, identity string, state AppliedState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t == nil || (t.expected != "" && identity != t.expected) || state.ComponentID != identity {
		return ErrAppliedStateUnexpected
	}
	if state.RouteGeneration == 0 || state.TrafficGeneration == 0 || state.RouteGeneration != state.TrafficGeneration {
		return ErrAppliedStateInvalid
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if prior, exists := t.states[identity]; exists && (state.RouteGeneration < prior.RouteGeneration || state.TrafficGeneration < prior.TrafficGeneration) {
		return ErrAppliedStateStale
	}
	state.ObservedAt = t.now().UTC()
	if _, exists := t.states[identity]; !exists {
		if len(t.order) >= maxAppliedStateEntries {
			oldest := t.order[0]
			t.order = t.order[1:]
			delete(t.states, oldest)
		}
		t.order = append(t.order, identity)
	}
	t.states[identity] = state
	return nil
}

// Applied reports an affirmative result only for the exact requested matched
// generation. Missing, unhealthy, split-generation, and stale reports fail
// closed without exposing edge internals.
func (t *AppliedStateTracker) Applied(ctx context.Context, route, traffic uint64) error {
	if t == nil || t.expected == "" {
		return ErrAppliedStateInvalid
	}
	return t.AppliedFor(ctx, t.expected, route, traffic)
}

// AppliedFor verifies the edge generated in a checkpoint without trusting a
// runtime-discovered identifier.
func (t *AppliedStateTracker) AppliedFor(ctx context.Context, componentID string, route, traffic uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t == nil || strings.TrimSpace(componentID) == "" || route == 0 || traffic == 0 || route != traffic {
		return ErrAppliedStateInvalid
	}
	t.mu.RLock()
	state, found := t.states[componentID]
	t.mu.RUnlock()
	if !found || !state.Healthy || state.RouteGeneration != route || state.TrafficGeneration != traffic {
		return ErrAppliedStateStale
	}
	return nil
}

func (t *AppliedStateTracker) Latest() (AppliedState, bool) {
	if t == nil {
		return AppliedState{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	state, ok := t.states[t.expected]
	return state, ok
}
