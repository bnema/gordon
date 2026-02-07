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

// ProxyDrainWaiter defines the contract for waiting until no in-flight
// requests remain for a container.
type ProxyDrainWaiter interface {
	WaitForNoInFlight(ctx context.Context, containerID string, timeout time.Duration) bool
}
