package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	edgesnapshotusecase "github.com/bnema/gordon/internal/usecase/edgesnapshot"
)

// migrationTrafficChecks is the production cutover reader. It consumes only
// sanitized runtime state and authenticated edge reports; it never creates a
// Docker-compatible client or opens a container socket.
type migrationTrafficChecks struct {
	state   out.RuntimeStateSubscriber
	store   *MigrationCheckpointStore
	applied *edgesnapshotusecase.AppliedStateTracker
}

func newMigrationTrafficChecks(state out.RuntimeStateSubscriber, store *MigrationCheckpointStore, applied *edgesnapshotusecase.AppliedStateTracker) (TrafficSwitchChecks, error) {
	if state == nil || store == nil || applied == nil {
		return nil, fmt.Errorf("runtime state, checkpoint store, and edge applied-state tracker are required")
	}
	return &migrationTrafficChecks{state: state, store: store, applied: applied}, nil
}

func (c *migrationTrafficChecks) ComponentHealthy(ctx context.Context, role domain.ComponentRole) error {
	checkpoint, err := c.store.Load()
	if err != nil {
		return fmt.Errorf("load migration checkpoint: %w", err)
	}
	plan, err := NewComponentLaunchPlan(*checkpoint)
	if err != nil {
		return err
	}
	component, found := componentForRole(plan, role)
	if !found {
		return fmt.Errorf("component %s is absent", role)
	}
	updates, err := c.state.SubscribeRuntimeState(ctx)
	if err != nil {
		return fmt.Errorf("subscribe runtime state: %w", err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case snapshot, open := <-updates:
		if !open {
			return fmt.Errorf("runtime state closed before component health")
		}
		for _, container := range snapshot.Containers {
			if container.Name == component.ComponentID && container.Status == domain.ContainerStatusRunning && container.Labels[domain.LabelComponentRole] == string(role) {
				return nil
			}
		}
		return fmt.Errorf("component %s is not running", role)
	}
}

// Authentication readiness is affirmative only for the edge report. The
// current runtime boundary has no equivalent authenticated readiness record
// for other roles, so refusing cutover is safer than inferring it from TCP.
func (c *migrationTrafficChecks) ComponentAuthenticationHealthy(ctx context.Context, role domain.ComponentRole) error {
	if role != domain.ComponentRoleEdge {
		return fmt.Errorf("authenticated %s readiness is unavailable", role)
	}
	checkpoint, err := c.store.Load()
	if err != nil {
		return err
	}
	plan, err := NewComponentLaunchPlan(*checkpoint)
	if err != nil {
		return err
	}
	edge, ok := componentForRole(plan, domain.ComponentRoleEdge)
	if !ok {
		return fmt.Errorf("edge component is absent")
	}
	return c.applied.AppliedFor(ctx, edge.ComponentID, checkpoint.RouteSnapshotGeneration, checkpoint.RouteSnapshotGeneration)
}

func (c *migrationTrafficChecks) AppliedRouteGeneration(ctx context.Context) (uint64, error) {
	return c.appliedGeneration(ctx)
}
func (c *migrationTrafficChecks) AppliedTrafficGeneration(ctx context.Context) (uint64, error) {
	return c.appliedGeneration(ctx)
}
func (c *migrationTrafficChecks) appliedGeneration(ctx context.Context) (uint64, error) {
	checkpoint, err := c.store.Load()
	if err != nil {
		return 0, err
	}
	plan, err := NewComponentLaunchPlan(*checkpoint)
	if err != nil {
		return 0, err
	}
	edge, ok := componentForRole(plan, domain.ComponentRoleEdge)
	if !ok || strings.TrimSpace(edge.ComponentID) == "" {
		return 0, fmt.Errorf("edge component is absent")
	}
	if err := c.applied.AppliedFor(ctx, edge.ComponentID, checkpoint.RouteSnapshotGeneration, checkpoint.RouteSnapshotGeneration); err != nil {
		return 0, err
	}
	return checkpoint.RouteSnapshotGeneration, nil
}

func (*migrationTrafficChecks) TestApplicationThroughEdge(context.Context) error {
	return fmt.Errorf("prepared edge application probe is unavailable")
}
func (*migrationTrafficChecks) TestRegistryV2ThroughEdge(context.Context) error {
	return fmt.Errorf("prepared edge registry probe is unavailable")
}
func (*migrationTrafficChecks) OldServingPathHealthy(context.Context, string) error {
	return fmt.Errorf("old serving path probe is unavailable")
}
