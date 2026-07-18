package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	edgesnapshotusecase "github.com/bnema/gordon/internal/usecase/edgesnapshot"
)

// TrafficSwitchChecks exposes only affirmative, split-deployment readiness
// facts. Implementations must test registry /v2 via the edge forwarding
// target, never edge-local localhost.
type TrafficSwitchChecks interface {
	ComponentHealthy(context.Context, domain.ComponentRole) error
	ComponentAuthenticationHealthy(context.Context, domain.ComponentRole) error
	AppliedRouteGeneration(context.Context) (uint64, error)
	AppliedTrafficGeneration(context.Context) (uint64, error)
	TestApplicationThroughEdge(context.Context) error
	TestRegistryV2ThroughEdge(context.Context) error
	OldServingPathHealthy(context.Context, string) error
}

// TrafficSwitcher keeps public cutover behind all fail-closed checks.
type TrafficSwitcher interface {
	Switch(context.Context, MigrationCheckpoint, ComponentLaunchPlan) error
}

type trafficSwitch struct {
	runtime out.RuntimeSelfUpdater
	checks  TrafficSwitchChecks
	now     func() time.Time
}

func NewTrafficSwitch(runtime out.RuntimeSelfUpdater, checks TrafficSwitchChecks) (TrafficSwitcher, error) {
	if runtime == nil || checks == nil {
		return nil, fmt.Errorf("runtime self-update client and traffic checks are required")
	}
	return &trafficSwitch{runtime: runtime, checks: checks, now: time.Now}, nil
}

// Switch activates the already healthy edge generation through the narrow
// runtime lifecycle protocol. It deliberately does not stop, remove, or alter
// the old serving path; retirement is a later, separately audited operation.
func (s *trafficSwitch) Switch(ctx context.Context, checkpoint MigrationCheckpoint, plan ComponentLaunchPlan) error {
	if s == nil || s.runtime == nil || s.checks == nil {
		return fmt.Errorf("traffic switch is not configured")
	}
	if checkpoint.Phase != MigrationPhasePrepared || checkpoint.ComponentGeneration == 0 || checkpoint.RouteSnapshotGeneration == 0 || strings.TrimSpace(checkpoint.OldServingPath) == "" {
		return fmt.Errorf("traffic switch requires a prepared checkpoint with route generation and old serving path")
	}
	if plan.Generation != checkpoint.ComponentGeneration || plan.MigrationID != checkpoint.MigrationID {
		return fmt.Errorf("traffic switch plan does not match checkpoint")
	}
	if err := s.verify(ctx, checkpoint, plan); err != nil {
		return err
	}
	edge, ok := componentForRole(plan, domain.ComponentRoleEdge)
	if !ok {
		return fmt.Errorf("traffic switch edge component is missing")
	}
	result, err := s.runtime.SelfUpdateRuntime(ctx, domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{
			ID:                domain.RuntimeCommandID("migration:" + checkpoint.MigrationID + ":switch:" + edge.ComponentID),
			IdempotencyKey:    "migration:" + checkpoint.MigrationID + ":switch:" + edge.ComponentID,
			Generation:        checkpoint.ComponentGeneration,
			SourceComponentID: "gordon-control",
			RequestedAt:       s.now().UTC(),
		},
		TargetComponentID: edge.ComponentID, TargetComponentRole: domain.ComponentRoleEdge,
		TargetVersion: edge.Labels[domain.LabelComponentVersion], Policy: domain.RuntimeSelfUpdatePolicyManualApproval,
		PolicyDecisionID: "migration:" + checkpoint.MigrationID,
		LifecycleAction:  domain.RuntimeComponentLifecycleActivate,
		DesiredImage:     edge.Image, DesiredStateHash: edge.DesiredStateHash, InternalNetwork: edge.InternalNetwork,
		EnvironmentFile: edge.EnvironmentFile, ConfigFile: edge.ConfigFile,
		PortPublishes:         append([]domain.ContainerPortPublish(nil), edge.PortPublishes...),
		OldServingComponentID: checkpoint.OldServingPath,
		FinalPortPublishes:    componentPublicPorts(checkpoint.PublicPortBindings, domain.ComponentRoleEdge),
		EdgeAppNetworks:       append([]string(nil), plan.AppNetworks...),
		PreserveVolumes:       true,
	})
	if err != nil {
		return fmt.Errorf("activate split edge through runtime: %w", err)
	}
	if result.Status != domain.RuntimeCommandStatusSucceeded {
		return fmt.Errorf("activate split edge through runtime was not accepted")
	}
	return nil
}

func (s *trafficSwitch) verify(ctx context.Context, checkpoint MigrationCheckpoint, plan ComponentLaunchPlan) error {
	for _, role := range []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleRuntime, domain.ComponentRoleRegistry, domain.ComponentRoleEdge} {
		component, ok := componentForRole(plan, role)
		if !ok || strings.TrimSpace(component.ComponentID) == "" {
			return fmt.Errorf("traffic switch %s component is missing", role)
		}
		if err := s.checks.ComponentHealthy(ctx, role); err != nil {
			return fmt.Errorf("traffic switch %s health prerequisite: %w", role, err)
		}
		if err := s.checks.ComponentAuthenticationHealthy(ctx, role); err != nil {
			return fmt.Errorf("traffic switch %s authentication prerequisite: %w", role, err)
		}
	}
	if err := s.waitForAppliedGeneration(ctx, checkpoint.RouteSnapshotGeneration); err != nil {
		return err
	}
	if err := s.checks.TestApplicationThroughEdge(ctx); err != nil {
		return fmt.Errorf("traffic switch application edge prerequisite: %w", err)
	}
	if err := s.checks.TestRegistryV2ThroughEdge(ctx); err != nil {
		return fmt.Errorf("traffic switch registry /v2 edge prerequisite: %w", err)
	}
	if err := s.checks.OldServingPathHealthy(ctx, checkpoint.OldServingPath); err != nil {
		return fmt.Errorf("traffic switch old serving path prerequisite: %w", err)
	}
	return nil
}

const trafficSwitchAppliedGenerationWait = 10 * time.Second

// waitForAppliedGeneration gives an asynchronously started edge a bounded
// opportunity to report an actual matching snapshot application. Only the
// explicit stale acknowledgement state is retried; malformed, mismatched, and
// transport errors still fail immediately and preserve the old serving path.
func (s *trafficSwitch) waitForAppliedGeneration(ctx context.Context, expected uint64) error {
	deadline := time.NewTimer(trafficSwitchAppliedGenerationWait)
	defer deadline.Stop()
	for {
		routeGeneration, routeErr := s.checks.AppliedRouteGeneration(ctx)
		if routeErr == nil && routeGeneration == expected {
			trafficGeneration, trafficErr := s.checks.AppliedTrafficGeneration(ctx)
			if trafficErr == nil && trafficGeneration == routeGeneration {
				return nil
			}
			if trafficErr == nil {
				return generationPrerequisiteError("traffic", nil)
			}
			if !errors.Is(trafficErr, edgesnapshotusecase.ErrAppliedStateStale) {
				return generationPrerequisiteError("traffic", trafficErr)
			}
		} else if routeErr == nil {
			return generationPrerequisiteError("route", nil)
		} else if !errors.Is(routeErr, edgesnapshotusecase.ErrAppliedStateStale) {
			return generationPrerequisiteError("route", routeErr)
		}
		select {
		case <-ctx.Done():
			return generationPrerequisiteError("route", ctx.Err())
		case <-deadline.C:
			return generationPrerequisiteError("route", edgesnapshotusecase.ErrAppliedStateStale)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func generationPrerequisiteError(name string, err error) error {
	if err != nil {
		return fmt.Errorf("traffic switch %s generation prerequisite: %w", name, err)
	}
	return fmt.Errorf("traffic switch %s generation prerequisite is not applied", name)
}

func componentForRole(plan ComponentLaunchPlan, role domain.ComponentRole) (ComponentLaunchComponent, bool) {
	for _, component := range plan.Components {
		if component.Role == role {
			return component, true
		}
	}
	return ComponentLaunchComponent{}, false
}
