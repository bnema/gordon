package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// ComponentRedeployExecutor is a narrow lifecycle protocol. It intentionally
// has no remove or volume operation: the previous generation remains usable
// until a separately authorized retirement operation after post-checks.
type ComponentRedeployExecutor interface {
	StartReplacement(context.Context, ComponentLaunchComponent) error
	CheckReplacementHealth(context.Context, ComponentLaunchComponent) error
	DrainPrevious(context.Context, ComponentLaunchComponent) error
	ActivateReplacement(context.Context, ComponentLaunchComponent) error
	PostCheck(context.Context, ComponentLaunchComponent) error
	RecoverRuntime(context.Context, ComponentLaunchComponent) error
}

type SelfRedeployReport struct {
	PreviousGenerationRetained bool
	Retryable                  bool
	FailedRole                 domain.ComponentRole
}

type SelfRedeployer struct{ executor ComponentRedeployExecutor }

func NewSelfRedeployer(executor ComponentRedeployExecutor) (*SelfRedeployer, error) {
	if executor == nil {
		return nil, fmt.Errorf("component redeploy executor is required")
	}
	return &SelfRedeployer{executor: executor}, nil
}

// Redeploy replaces the next component generation in the only safe order:
// registry, drained edge, checkpointed control, then runtime with recovery.
// Every failure leaves the prior generation untouched and reports a retryable
// state. No path in this operation deletes a container or persistent volume.
func (r *SelfRedeployer) Redeploy(ctx context.Context, previous, desired ComponentLaunchPlan) (SelfRedeployReport, error) {
	report := SelfRedeployReport{PreviousGenerationRetained: true}
	if r == nil || r.executor == nil {
		return report, fmt.Errorf("self redeployer is not configured")
	}
	if err := validRedeployPlans(previous, desired); err != nil {
		return report, err
	}
	for _, role := range []domain.ComponentRole{domain.ComponentRoleRegistry, domain.ComponentRoleEdge, domain.ComponentRoleControl, domain.ComponentRoleRuntime} {
		oldComponent, _ := componentForRole(previous, role)
		nextComponent, _ := componentForRole(desired, role)
		if err := r.replaceOne(ctx, oldComponent, nextComponent); err != nil {
			report.Retryable, report.FailedRole = true, role
			return report, fmt.Errorf("self redeploy %s: %w", role, err)
		}
	}
	return report, nil
}

func validRedeployPlans(previous, desired ComponentLaunchPlan) error {
	if previous.Generation == 0 || desired.Generation <= previous.Generation || previous.MigrationID == "" || previous.MigrationID != desired.MigrationID {
		return fmt.Errorf("self redeploy requires consecutive Gordon generations for one migration")
	}
	for _, role := range []domain.ComponentRole{domain.ComponentRoleRegistry, domain.ComponentRoleEdge, domain.ComponentRoleControl, domain.ComponentRoleRuntime} {
		oldComponent, oldOK := componentForRole(previous, role)
		nextComponent, nextOK := componentForRole(desired, role)
		if !oldOK || !nextOK || oldComponent.ComponentID == nextComponent.ComponentID || !strings.HasPrefix(oldComponent.ComponentID, "gordon-") || !strings.HasPrefix(nextComponent.ComponentID, "gordon-") {
			return fmt.Errorf("self redeploy %s component identity is invalid", role)
		}
	}
	return nil
}

func (r *SelfRedeployer) replaceOne(ctx context.Context, previous, desired ComponentLaunchComponent) error {
	if err := r.executor.StartReplacement(ctx, desired); err != nil {
		return fmt.Errorf("start replacement: %w", err)
	}
	if err := r.executor.CheckReplacementHealth(ctx, desired); err != nil {
		return fmt.Errorf("check replacement health: %w", err)
	}
	if desired.Role == domain.ComponentRoleEdge {
		if err := r.executor.DrainPrevious(ctx, previous); err != nil {
			return fmt.Errorf("drain previous edge: %w", err)
		}
	}
	if err := r.executor.ActivateReplacement(ctx, desired); err != nil {
		return fmt.Errorf("activate replacement: %w", err)
	}
	if err := r.executor.PostCheck(ctx, desired); err != nil {
		return fmt.Errorf("post-check replacement: %w", err)
	}
	if desired.Role == domain.ComponentRoleRuntime {
		if err := r.executor.RecoverRuntime(ctx, desired); err != nil {
			return fmt.Errorf("recover runtime replacement: %w", err)
		}
	}
	return nil
}

// RuntimeComponentRedeployExecutor realizes each protocol operation through
// RuntimeSelfUpdate. It owns no socket and accepts no engine-specific flags.
type RuntimeComponentRedeployExecutor struct {
	runtime out.RuntimeSelfUpdater
	now     func() time.Time
}

func NewRuntimeComponentRedeployExecutor(runtime out.RuntimeSelfUpdater) (*RuntimeComponentRedeployExecutor, error) {
	if runtime == nil {
		return nil, fmt.Errorf("runtime self-update client is required")
	}
	return &RuntimeComponentRedeployExecutor{runtime: runtime, now: time.Now}, nil
}
func (e *RuntimeComponentRedeployExecutor) StartReplacement(ctx context.Context, c ComponentLaunchComponent) error {
	return e.send(ctx, c, domain.RuntimeComponentLifecycleReplace, "replace")
}
func (e *RuntimeComponentRedeployExecutor) CheckReplacementHealth(ctx context.Context, c ComponentLaunchComponent) error {
	return e.send(ctx, c, domain.RuntimeComponentLifecycleHealth, "health")
}
func (e *RuntimeComponentRedeployExecutor) DrainPrevious(ctx context.Context, c ComponentLaunchComponent) error {
	return e.send(ctx, c, domain.RuntimeComponentLifecycleDrain, "drain")
}
func (e *RuntimeComponentRedeployExecutor) ActivateReplacement(ctx context.Context, c ComponentLaunchComponent) error {
	return e.send(ctx, c, domain.RuntimeComponentLifecycleActivate, "activate")
}
func (e *RuntimeComponentRedeployExecutor) PostCheck(ctx context.Context, c ComponentLaunchComponent) error {
	return e.send(ctx, c, domain.RuntimeComponentLifecycleHealth, "postcheck")
}
func (e *RuntimeComponentRedeployExecutor) RecoverRuntime(ctx context.Context, c ComponentLaunchComponent) error {
	return e.send(ctx, c, domain.RuntimeComponentLifecycleHealth, "recover")
}
func (e *RuntimeComponentRedeployExecutor) send(ctx context.Context, c ComponentLaunchComponent, action domain.RuntimeComponentLifecycleAction, operation string) error {
	command, err := newComponentLifecycleCommand(c, action, componentMigrationID(c), operation, e.now())
	if err != nil {
		return err
	}
	result, err := e.runtime.SelfUpdateRuntime(ctx, command)
	if err != nil {
		return fmt.Errorf("send component %s command: %w", operation, err)
	}
	if result.Status != domain.RuntimeCommandStatusSucceeded {
		return fmt.Errorf("component %s command was not accepted", operation)
	}
	return nil
}
