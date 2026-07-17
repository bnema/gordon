package out

import (
	"context"
	"time"
)

// ProxyCacheInvalidator defines the contract for synchronously invalidating
// proxy target cache entries. This is used during zero-downtime deployments
// to ensure the proxy stops routing to an old container before it is stopped.
type ProxyCacheInvalidator interface {
	InvalidateTarget(ctx context.Context, domainName string)
}

// ProxyDrainWaiter defines the contract for safely draining a retired
// container. PrepareDrain must run before the traffic switch; it pins the old
// target identity until WaitForNoInFlight or CancelDrain releases it.
type ProxyDrainWaiter interface {
	PrepareDrain(containerID string)
	CancelDrain(containerID string)
	WaitForNoInFlight(ctx context.Context, containerID string, timeout time.Duration) bool
}
