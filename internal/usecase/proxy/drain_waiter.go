package proxy

import (
	"context"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
)

// LocalSnapshotDrainWaiter translates the container lifecycle's private
// runtime ID into the opaque key used by snapshot-derived proxy targets.
type LocalSnapshotDrainWaiter struct {
	provider *LocalSnapshotProvider
	service  *Service
}

var _ out.ProxyDrainWaiter = (*LocalSnapshotDrainWaiter)(nil)

// NewLocalSnapshotDrainWaiter creates the monolith lifecycle drain adapter.
func NewLocalSnapshotDrainWaiter(provider *LocalSnapshotProvider, service *Service) *LocalSnapshotDrainWaiter {
	return &LocalSnapshotDrainWaiter{provider: provider, service: service}
}

// WaitForNoInFlight waits by opaque key. An unmapped old ID cannot have been
// tracked by the snapshot proxy, so succeeding is both safe and compatible
// with the prior no-in-flight behavior. Once the key is found, its private
// association is released on success, timeout, or context cancellation.
func (w *LocalSnapshotDrainWaiter) WaitForNoInFlight(ctx context.Context, containerID string, timeout time.Duration) bool {
	if w == nil || w.provider == nil || w.service == nil || containerID == "" {
		return true
	}
	key, ok := w.provider.TargetKeyForContainer(containerID)
	if !ok {
		return true
	}
	defer w.provider.ReleaseTargetKeyForContainer(containerID)
	return w.service.WaitForNoInFlight(ctx, string(key), timeout)
}
