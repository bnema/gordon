package deployment

import (
	"context"
	"fmt"
	"sort"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

// reconcileRemovalsForDeploy journals and applies the removal of services
// the new revision no longer declares. A failure marks the operation
// failed and aborts before any new state is published.
func (s *Service) reconcileRemovalsForDeploy(ctx context.Context, app string, active domain.AppActive, pinned []pinnedService, op *domain.AppOperation, result *DeployResult, log zerowrap.Logger) error {
	removalSteps, removed, warnings, err := s.reconcileRemovedServices(ctx, app, active, pinned)
	op.Warnings = append(op.Warnings, journalWarnings(warnings)...)
	result.CleanupWarnings = append(result.CleanupWarnings, warnings...)
	if err != nil {
		op.Steps = append(op.Steps, removalSteps...)
		op.Outcome = domain.AppOutcomeFailed
		if saveErr := s.deps.State.SaveOperation(ctx, *op); saveErr != nil {
			log.Warn().Err(saveErr).Msg("deployment: failed to record removal failure")
		}
		return err
	}
	if len(removalSteps) == 0 {
		return nil
	}
	op.Steps = append(op.Steps, removalSteps...)
	result.Removed = removed
	if saveErr := s.deps.State.SaveOperation(ctx, *op); saveErr != nil {
		log.Warn().Err(saveErr).Msg("deployment: failed to record service removals")
	}
	return nil
}

// reconcileRemovedServices withdraws and retires every ACTIVE service the
// new revision no longer declares. Traffic is withdrawn first (fail
// closed), then the exact container is inhibited, stopped, and removed
// with its data retained, its backend claims released, and its ACTIVE
// entry deleted and persisted before its recovery inhibition is cleared. A failure leaves the remaining services untouched and
// stops the deploy before any new state is published.
func (s *Service) reconcileRemovedServices(ctx context.Context, app string, active domain.AppActive, pinned []pinnedService) ([]domain.AppOperationStep, []string, []CleanupWarning, error) {
	desired := make(map[string]struct{}, len(pinned))
	for _, p := range pinned {
		desired[p.name] = struct{}{}
	}
	var names []string
	for name := range active.Services {
		if _, ok := desired[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	steps := make([]domain.AppOperationStep, 0, len(names))
	var cleanupWarnings []CleanupWarning
	for _, name := range names {
		eff := active.Services[name]
		step, warnings, err := s.retireRemovedService(ctx, app, name, eff)
		steps = append(steps, step)
		cleanupWarnings = append(cleanupWarnings, warnings...)
		if err != nil {
			return steps, names, cleanupWarnings, err
		}
		// Persist the removal before dropping the recovery inhibition: an
		// ACTIVE record that still lists the service without its
		// inhibition would let recovery recreate a removed service.
		delete(active.Services, name)
		if err := s.deps.State.SaveActive(ctx, active); err != nil {
			return steps, names, cleanupWarnings, fmt.Errorf("deployment: persist service removal %q: %w", name, err)
		}
		if err := s.clearRecoveryInhibition(ctx, app, name, eff.Container); err != nil {
			cleanupWarnings = append(cleanupWarnings, CleanupWarning{
				Service: name, Leftover: eff.Container, Detail: "clear recovery inhibition: " + err.Error(),
			})
		}
	}
	if len(names) == 0 {
		return nil, nil, nil, nil
	}
	return steps, names, cleanupWarnings, nil
}

// retireRemovedService withdraws one removed service and removes its exact
// container, releasing its claims only after withdrawal succeeded.
func (s *Service) retireRemovedService(ctx context.Context, app, name string, eff domain.AppEffectiveService) (domain.AppOperationStep, []CleanupWarning, error) {
	step := domain.AppOperationStep{
		ID:      "service." + name + ".remove",
		State:   domain.AppStepPending,
		Service: name,
		Before:  eff.Container,
	}
	fail := func(err error) (domain.AppOperationStep, []CleanupWarning, error) {
		step.State = domain.AppStepFailed
		step.Error = err.Error()
		return step, nil, err
	}
	if err := s.withdrawForRecovery(ctx, app, name); err != nil {
		return fail(err)
	}
	if eff.Container != "" {
		if err := s.inhibitRecovery(ctx, app, name, eff.Container, "removed", ""); err != nil {
			return fail(err)
		}
		// The inhibition stays until the caller persisted the removal.
		retired := s.retireContainer(ctx, app, retireOptions{
			Service: name, Grace: serviceStopGrace(eff),
		}, eff.Container)
		if !retired.Gone {
			return fail(fmt.Errorf("deployment: remove %s/%s container %s: %s", app, name, eff.Container, cleanupDetail(retired)))
		}
		step.State = domain.AppStepSucceeded
		step.After = ""
		return step, retired.Warnings, nil
	}
	step.State = domain.AppStepSucceeded
	step.After = ""
	return step, nil, nil
}
