package out

import "context"

// RouteChangeWatcher is the outbound port for watching route changes.
// It is implemented by the CoreService gRPC client in the proxy component.
type RouteChangeWatcher interface {
	// Watch listens for route change events from the core.
	// The onInvalidate callback is called when a route is invalidated or deleted.
	// This should be called in a goroutine as it blocks until context is cancelled.
	Watch(ctx context.Context, onInvalidate func(domain string)) error
}
