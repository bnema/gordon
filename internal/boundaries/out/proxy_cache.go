package out

import "context"

// ProxyCacheInvalidator defines the contract for synchronously invalidating
// proxy target cache entries. This is used during zero-downtime deployments
// to ensure the proxy stops routing to an old container before it is stopped.
type ProxyCacheInvalidator interface {
	InvalidateTarget(ctx context.Context, domainName string)
}
