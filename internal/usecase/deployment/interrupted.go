package deployment

import (
	"context"
	"fmt"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

// reconcileInterruptedDeploy converges the journal of a deployment that a
// crash, shutdown, or failed cleanup interrupted. A candidate that was
// created but never published is removed and its loopback claims released, so
// recovery can rebuild the published generation without ever running two
// generations of one service. The interrupted operation is then finalized as
// failed: a repeat of its key replays an explicit failure instead of a
// permanent in-flight claim.
//
// It also retries the candidates of a failed operation whose cleanup did not
// complete, so a leftover stays visible and is eventually removed. The
// published generation recorded in ACTIVE is never touched.
//
// It runs at boot and before every mutation of the app, always under the app
// lock, so a non-terminal journal is definitively interrupted, and a mutation
// that cannot reconcile a predecessor does not proceed. It reads the
// operation journal first and only then ACTIVE, so the common case of a
// successful operation costs one state read.
//
// Reconciliation fails closed on a candidate that cannot be removed: an
// orphan that may still run aborts the caller instead of being journaled as a
// recoverable leftover. Because the journal is never finalized while such a
// leftover exists, it stays the latest operation and a newer operation can
// never be created on top of it and mask it.
func (s *Service) reconcileInterruptedDeploy(ctx context.Context, app string) error {
	op, ok, err := s.deps.State.LoadLatestOperation(ctx, app)
	if err != nil {
		return fmt.Errorf("deployment: load interrupted operation for %q: %w", app, err)
	}
	if !ok {
		return nil
	}
	if !op.Terminal() && s.operationLive(app, op.Op) {
		// The operation is owned by a live goroutine in this process that
		// may still be persisting its outcome. Reconcile it only after that
		// owner has exited; until then the store record is authoritative and
		// must not be finalized here.
		return nil
	}
	terminal := op.Terminal()
	needsActive := false
	for _, step := range op.Steps {
		if reconcileNeedsContainer(step, terminal) {
			needsActive = true
			break
		}
	}
	var active domain.AppActive
	if needsActive {
		active, _, err = s.deps.State.LoadActive(ctx, app)
		if err != nil {
			return fmt.Errorf("deployment: load active for interrupted operation: %w", err)
		}
	}
	log := zerowrap.FromCtx(ctx)
	// write is true when the journal itself changes: an interrupted operation
	// is always finalized, a terminal one only when a leftover must be
	// reported.
	changed, err := s.convergeInterruptedSteps(ctx, app, &op, active, terminal, log)
	if err != nil {
		return err
	}
	write := !terminal || changed
	if !write {
		return nil
	}
	if !terminal {
		// The effects ran and only the outcome write was interrupted: that is
		// a success, not a failure.
		op.Outcome = interruptedOutcome(op)
	}
	if err := s.deps.State.SaveOperation(ctx, op); err != nil {
		return fmt.Errorf("deployment: finalize interrupted operation for %q: %w", app, err)
	}
	log.Warn().Str("app", app).Str("op", op.Op).Msg("deployment: reconciled an unfinished operation")
	return nil
}

// reconcileNeedsContainer reports whether one step still needs container
// work: a step that recorded a candidate. A terminal operation only retries
// the step that recorded a failed candidate; a pending step exists only in an
// interrupted operation.
func reconcileNeedsContainer(step domain.AppOperationStep, terminal bool) bool {
	if step.After == "" {
		return false
	}
	if terminal {
		return step.State == domain.AppStepFailed
	}
	return true
}

// convergeInterruptedSteps retries the recorded candidate of every step that
// needs container work and accumulates the bounded leftovers in the journal.
// It reports whether the journal changed and fails closed on the first
// candidate that cannot be removed, before any later step or mutation runs.
func (s *Service) convergeInterruptedSteps(ctx context.Context, app string, op *domain.AppOperation, active domain.AppActive, terminal bool, log zerowrap.Logger) (bool, error) {
	changed := false
	for i := range op.Steps {
		step := &op.Steps[i]
		if !reconcileNeedsContainer(*step, terminal) {
			continue
		}
		warnings, convErr := s.convergeCandidate(ctx, app, active, step, terminal, log)
		if step.After == "" {
			// Converging the candidate clears its recorded After, so the
			// journal changed even when nothing was left over to report: the
			// converged step must be persisted, or every later pass reconciles
			// it again.
			changed = true
		}
		if len(warnings) > 0 {
			// A candidate that will not go away is operator-visible instead of
			// staying untracked.
			op.Warnings = append(op.Warnings, journalWarnings(warnings)...)
			changed = true
		}
		if convErr != nil {
			// Fail closed: the orphan may still be running, so no later
			// mutation may proceed. The journal keeps the leftover (and the
			// failed step) for the next pass; a terminal operation stays the
			// latest record because no newer claim is opened.
			if saveErr := s.deps.State.SaveOperation(ctx, *op); saveErr != nil {
				log.Warn().Err(saveErr).Msg("deployment: failed to record reconciliation failure")
			}
			return changed, fmt.Errorf("deployment: reconcile incomplete operation for %q: %w", app, convErr)
		}
	}
	return changed, nil
}

// convergeCandidate converges the container one step recorded. The published
// generation is kept; a never-published candidate is removed so recovery can
// rebuild the generation recorded in ACTIVE without two generations running
// at once. An interrupted operation's step is marked failed.
//
// A candidate that cannot be removed is returned as an error, not a
// recoverable warning: the orphan may still run, so every later mutation must
// fail closed instead of proceeding. The returned warnings are the leftovers
// of that removal.
//
// Success clears the step's recorded After and keeps the removed (or kept)
// container id in Detail, so the audit trail survives while the converged step
// is no longer retried on every later pass. Only the failure paths leave After
// in place, keeping a failed convergence eligible for retry.
//
// Once the candidate is converged, the inhibition the replacement wrote for
// the superseded container is cleared. A step carrying both Before and After
// records that the superseded container was confirmed gone before the
// candidate was created (deployService retires it first), so the marker
// protects nothing and would otherwise refuse the boot/start/restart rebuild
// from ACTIVE.
func (s *Service) convergeCandidate(ctx context.Context, app string, active domain.AppActive, step *domain.AppOperationStep, terminal bool, log zerowrap.Logger) ([]CleanupWarning, error) {
	candidate := step.After
	if publishedContainer(active, candidate) {
		// The published generation: keep it. A step that already succeeded is
		// left as recorded; a routing apply that never ran is republished by
		// the periodic recovery pass.
		if !terminal && step.State != domain.AppStepSucceeded {
			step.State = domain.AppStepFailed
			step.Error = "interrupted before traffic publication; the published container is kept and routing is republished"
		}
		step.Detail = "converged: published container " + candidate + " kept"
	} else {
		retired := s.retireContainer(ctx, app, retireOptions{Service: step.Service, Force: true}, candidate)
		if !retired.Gone {
			if !terminal {
				step.State = domain.AppStepFailed
				step.Error = "interrupted before publication; the unpublished candidate could not be removed: " + cleanupDetail(retired)
				log.Warn().Str("app", app).Str("container", candidate).Msg("deployment: unpublished candidate could not be removed")
			}
			return retired.Warnings, fmt.Errorf("deployment: candidate %s of service %q could not be removed: %s", candidate, step.Service, cleanupDetail(retired))
		}
		if !terminal {
			step.State = domain.AppStepFailed
			step.Error = "interrupted before publication; the unpublished candidate was removed"
		}
		step.Detail = "converged: removed unpublished candidate " + candidate
	}
	// The candidate is gone (or published): the replacement-pending
	// inhibition of the superseded container is stale. Dropping it lets
	// boot/start/restart rebuild that generation from ACTIVE; the single
	// writer is preserved because the superseded container was proven gone
	// before the candidate existed.
	if step.Service != "" && step.Before != "" && step.Before != candidate {
		if err := s.clearRecoveryInhibition(ctx, app, step.Service, step.Before); err != nil {
			log.Warn().Err(err).Str("app", app).Str("container", step.Before).Msg("deployment: clear stale replacement inhibition")
			return []CleanupWarning{{Service: step.Service, Leftover: step.Before, Detail: "clear recovery inhibition: " + err.Error()}}, nil
		}
	}
	// Only reconciliation removes a recorded candidate: clearing After here is
	// what marks the step converged.
	step.After = ""
	return nil, nil
}

// interruptedOutcome classifies an interrupted operation: a failure, unless
// every step had already succeeded.
func interruptedOutcome(op domain.AppOperation) string {
	if operationSucceeded(op) {
		return domain.AppOutcomeSuccess
	}
	return domain.AppOutcomeFailed
}

// operationSucceeded reports whether every step of an interrupted operation
// reached success: its effects ran and only the final outcome write was
// interrupted.
func operationSucceeded(op domain.AppOperation) bool {
	if len(op.Steps) == 0 {
		return false
	}
	for _, step := range op.Steps {
		if step.State != domain.AppStepSucceeded {
			return false
		}
	}
	return true
}

// publishedContainer reports whether any service of the ACTIVE record names
// the container. A container that is the published generation is never a
// leftover, whichever service recorded it.
func publishedContainer(active domain.AppActive, containerID string) bool {
	if containerID == "" {
		return false
	}
	for _, service := range active.Services {
		if service.Container == containerID {
			return true
		}
	}
	return false
}
