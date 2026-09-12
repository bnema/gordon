package in

import "context"

// AppReconciler is the daemon-only driving port for periodic app
// recovery. It is deliberately absent from AppService and from any
// HTTP or CLI surface: only the daemon-owned monitor calls it.
//
// ReconcileRunning converges ACTIVE workloads to durable intent. It
// never selects content from DESIRED, never changes intent, and never
// pulls, creates, or removes a container.
type AppReconciler interface {
	// ReconcileRunning runs one deterministic recovery pass over every
	// known app. Busy apps are skipped, independent per-service
	// deadlines bound the work, and per-app errors are aggregated.
	ReconcileRunning(ctx context.Context) error
}
