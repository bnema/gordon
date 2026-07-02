package in

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// RuntimeWorker handles narrow runtime intent commands from the control plane.
type RuntimeWorker interface {
	DeployRoute(ctx context.Context, command domain.DeployRouteCommand) (domain.RuntimeCommandResult, error)
	RestartRoute(ctx context.Context, command domain.RestartRouteCommand) (domain.RuntimeCommandResult, error)
	RemoveRoute(ctx context.Context, command domain.RemoveRouteCommand) (domain.RuntimeCommandResult, error)
	Reconcile(ctx context.Context, command domain.ReconcileRuntimeCommand) (domain.RuntimeCommandResult, error)
	SelfUpdate(ctx context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error)
}
