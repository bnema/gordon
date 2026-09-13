package deployment

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

// failLogTailLines bounds the log tail attached to service failures.
const failLogTailLines = 50

// failLogTailTimeout bounds the diagnostics read of a failing container.
const failLogTailTimeout = 10 * time.Second

// Deploy executes a preflighted revision: fail-fast across services in
// sorted name order. Every service is replaced sequentially — withdraw
// traffic, retire the superseded container, start the replacement and wait
// for its readiness probe — so a deployment may briefly interrupt the
// service, and two Gordon-managed generations of one service never run at
// the same time. Volumes are never deleted: no RemoveVolume call, no
// volume-deletion flags on container removal.
//
// It is the synchronous composition of the two engine phases: StartDeploy
// claims the key and journals the operation, then ExecuteDeploy runs the
// effects for the owner only.
func (s *Service) Deploy(ctx context.Context, input DeployInput) (*DeployResult, error) {
	started, err := s.StartDeploy(ctx, input)
	if err != nil {
		return nil, err
	}
	if !started.Owned {
		// The key already answered this request: its stored journal is the
		// result and no workload is touched again.
		return journaledOrNil(input, &started.Claim.Journal, started.ReplayError())
	}
	return s.ExecuteDeploy(ctx, started.Claim)
}

// DeployClaim is the durable identity of one started deploy operation. It is
// the hand-off between the two phases: StartDeploy returns it and
// ExecuteDeploy consumes it. App and Op are the durable identity, so a
// caller that reconstructs the claim from them alone (for example after a
// restart, with no in-process state) leaves Journal and Resolved empty and
// ExecuteDeploy loads and resolves both from the store.
type DeployClaim struct {
	App      string
	Op       string
	Service  string
	Revision string
	// Resolved is the revision StartDeploy resolved for this claim. Empty
	// when the claim was reconstructed from identity alone.
	Resolved domain.AppDesiredRevision
	// Journal is the claimed operation held in process. Its Op is empty when
	// the claim was reconstructed from identity alone.
	Journal domain.AppOperation
}

// StartDeployResult reports whether this caller owns the claimed operation.
// Owned is true only for the single caller that claimed Op. Every idempotent
// replay (the same key already answered) reports Owned false, and the stored
// journal in Claim.Journal is the result. Only an owner may pass the claim to
// ExecuteDeploy.
type StartDeployResult struct {
	Claim DeployClaim
	Owned bool
}

// ReplayError is the explicit disposition of a claim this caller does not
// own: nil for a terminal success, and a conflict for an in-flight,
// interrupted, or failed operation, so a replay is never mistaken for a
// second execution. It is nil for an owned operation.
func (r StartDeployResult) ReplayError() error {
	if r.Owned {
		return nil
	}
	return replayError(r.Claim.Journal)
}

// StartDeploy performs the first phase of a deploy. Under the app lock it
// converges any interrupted predecessor, resolves the request to a revision
// and runs the targeted-deploy convergence checks, atomically claims the
// request key, and persists the non-terminal journal. It performs no image
// pull, pinning, or workload mutation, so a crash right after it leaves a
// durable claim that reconciliation can converge.
//
// The returned claim distinguishes the newly owned operation (Owned true)
// from an idempotent replay (Owned false): the same key is never handed to
// two owners, and a replay never executes effects again.
func (s *Service) StartDeploy(ctx context.Context, input DeployInput) (*StartDeployResult, error) {
	// Planning and claiming acquire no runtime resource, so StartDeploy takes
	// only the per-app coordinator. ExecuteDeploy takes the GC shared lease
	// across resource acquisition and publication, where it is required.
	release, err := s.coord.acquire(ctx, input.App)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.startDeployLocked(ctx, input)
}

func (s *Service) startDeployLocked(ctx context.Context, input DeployInput) (*StartDeployResult, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "StartDeploy",
		"app":                 input.App,
	})
	log := zerowrap.FromCtx(ctx)

	if err := s.deps.State.Recover(ctx); err != nil {
		return nil, fmt.Errorf("deployment: recover before deploy: %w", err)
	}
	// A mutation of this app never starts on top of an interrupted one: its
	// never-published candidates must be gone before this mutation creates a
	// generation of the same service. ACTIVE is materialized first, so the
	// published generation cannot be mistaken for a leftover.
	if err := s.reconcileInterruptedDeploy(ctx, input.App); err != nil {
		return nil, err
	}
	rev, op, owned, err := s.claimDeploymentLocked(ctx, input)
	if err != nil {
		return nil, err
	}
	if owned {
		// The claimed operation is now live in this process. It is
		// registered before the caller releases the coordinator, so a
		// foreground mutation that takes the lock in the gap before
		// ExecuteDeploy cannot reconcile the claim away.
		s.markOperationLive(input.App, op.Op)
	} else {
		log.Info().Str("op", op.Op).Msg("deployment: replayed an already claimed operation")
	}
	return &StartDeployResult{
		Claim: DeployClaim{
			App: input.App, Op: op.Op, Service: input.Service, Revision: input.Revision,
			Resolved: rev, Journal: op,
		},
		Owned: owned,
	}, nil
}

// ExecuteDeploy performs the second phase of a deploy: it reacquires the app
// lock, loads and verifies the claimed journal by app/op identity, runs the
// image pull/pin and the sequential replacement, and records the terminal
// outcome. A claim that is already terminal, or that does not match its
// identity, is never executed: it replays its stored outcome instead. An
// operation that stops mid-execution (shutdown or cancellation) leaves its
// non-terminal journal behind for reconciliation, so it can never run twice.
//
// The operation stays live until this returns: the store record, not the
// in-process claim, is what executes, and the live marker is cleared only
// after the outcome was recorded or a recoverable non-terminal claim was
// left behind, while the app lock is still held.
func (s *Service) ExecuteDeploy(ctx context.Context, claim DeployClaim) (*DeployResult, error) {
	release, err := s.acquireAppContext(ctx, claim.App)
	if err != nil {
		// The claim is left as persisted: recoverable, but no longer owned
		// here, so a later reconciliation can converge it.
		s.clearOperationLive(claim.App, claim.Op)
		return nil, err
	}
	defer release()
	// Registered after release, so it runs first: the marker is dropped
	// before the app lock is released, and never while a live goroutine may
	// still persist state.
	defer s.clearOperationLive(claim.App, claim.Op)
	return s.executeLocked(ctx, claim)
}

// executeLocked runs the claimed, non-terminal operation. The caller holds
// the app lock. Every failure that happens before a terminal outcome is
// recorded (load active, revision, service step, traffic) leaves the journal
// with its step state, never a silent success.
func (s *Service) executeLocked(ctx context.Context, claim DeployClaim) (*DeployResult, error) {
	input := DeployInput{App: claim.App, Revision: claim.Revision, Service: claim.Service}
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "ExecuteDeploy",
		"app":                 claim.App,
		"op":                  claim.Op,
	})
	log := zerowrap.FromCtx(ctx)

	op, err := s.loadClaim(ctx, claim)
	if err != nil {
		return nil, err
	}
	if op.Terminal() {
		// The claim was finalized between the phases: never execute a settled
		// key, replay its stored outcome instead.
		return journaledDeployResult(input, &op), replayError(op)
	}
	rev, err := s.claimRevision(ctx, claim, op)
	if err != nil {
		s.failOperation(ctx, &op, err, "error")
		return journaledDeployResult(input, &op), err
	}
	pinned, err := s.pinPreflightLocked(ctx, input.App, input.Service, rev, &op)
	if err != nil {
		return journaledDeployResult(input, &op), err
	}
	sort.Slice(pinned, func(i, j int) bool { return pinned[i].name < pinned[j].name })

	active, _, err := s.deps.State.LoadActive(ctx, input.App)
	if err != nil {
		loadErr := fmt.Errorf("deployment: load active: %w", err)
		s.failOperation(ctx, &op, loadErr, "error")
		return journaledDeployResult(input, &op), loadErr
	}
	rev, err = s.resolveRevision(ctx, input)
	if err != nil {
		s.failOperation(ctx, &op, err, "error")
		return journaledDeployResult(input, &op), err
	}

	result := &DeployResult{
		Op:       op.Op,
		App:      input.App,
		Revision: rev.Revision,
		Services: map[string]ServiceResult{},
	}
	// A targeted deploy is intentionally a partial plan: services omitted
	// from pinned remain active. Full deploys reconcile actual removals.
	if input.Service == "" {
		if err := s.reconcileRemovalsForDeploy(ctx, input.App, active, pinned, &op, result, log); err != nil {
			return result, err
		}
	}
	for i, p := range pinned {
		if err := s.runServiceStep(ctx, input.App, rev.Revision, i, p, &op, active, result, log); err != nil {
			return result, err
		}
	}
	op.Outcome = ComputeOutcome(result.Services)
	op.Warnings = journalWarnings(collectCleanupWarnings(result.Services))
	result.CleanupWarnings = collectCleanupWarnings(result.Services)
	if saveErr := s.deps.State.SaveOperation(ctx, op); saveErr != nil {
		log.Warn().Err(saveErr).Msg("deployment: failed to record deploy outcome")
	}
	return result, nil
}

// loadClaim verifies the claimed journal by app/op identity. The store is
// authoritative: the operation is always reloaded by app/op ID, so a stale
// in-process journal can never be executed (for example when another actor
// finalized the key between the claim and execution). A journal that does
// not match the identity, or is not a deploy, is refused.
func (s *Service) loadClaim(ctx context.Context, claim DeployClaim) (domain.AppOperation, error) {
	if claim.App == "" || claim.Op == "" {
		return domain.AppOperation{}, fmt.Errorf("deployment: execute requires an app and operation identity: %w", domain.ErrAppStateConflict)
	}
	op, err := s.deps.State.LoadOperation(ctx, claim.App, claim.Op)
	if err != nil {
		return domain.AppOperation{}, fmt.Errorf("deployment: load claimed operation %q: %w", claim.Op, err)
	}
	if op.App != claim.App || op.Op != claim.Op {
		return domain.AppOperation{}, fmt.Errorf("deployment: claim does not identify app %q operation %q: %w", claim.App, claim.Op, domain.ErrAppStateConflict)
	}
	if op.Kind != "deploy" {
		return domain.AppOperation{}, fmt.Errorf("deployment: operation %q is %q, not a deploy: %w", claim.Op, op.Kind, domain.ErrAppStateConflict)
	}
	return op, nil
}

// claimRevision returns the revision StartDeploy captured. A claim
// reconstructed from identity alone resolves the revision its journal
// recorded, so a resumed execution pins the revision it was started for.
func (s *Service) claimRevision(ctx context.Context, claim DeployClaim, op domain.AppOperation) (domain.AppDesiredRevision, error) {
	if claim.Resolved.Revision != "" {
		return claim.Resolved, nil
	}
	return s.resolveRevision(ctx, DeployInput{App: claim.App, Revision: op.InputRevision, Service: claim.Service})
}

// runServiceStep executes one pinned service in the journal: replace it,
// publish its ACTIVE entry, then publish traffic for the new container. The
// step is recorded as succeeded only after the runtime effect, the ACTIVE
// publication, and the traffic apply all succeeded, so a journaled success
// is never ahead of what is routed.
func (s *Service) runServiceStep(
	ctx context.Context,
	app, revision string,
	index int,
	p pinnedService,
	op *domain.AppOperation,
	active domain.AppActive,
	result *DeployResult,
	log zerowrap.Logger,
) error {
	before := ""
	if eff, ok := active.Services[p.name]; ok {
		before = eff.Container
	}
	stepID := "service." + p.name + ".replace"
	// The step is journaled before the replacement runs and carries the
	// candidate's container ID as soon as it exists, so an interrupted deploy
	// leaves a trace boot recovery can clean up instead of an untracked
	// generation.
	step := &op.Steps[index+1]
	*step = domain.AppOperationStep{
		ID: stepID, Service: p.name,
		Digest: p.digest, Image: p.runtimeImage,
		Before: before, State: domain.AppStepPending,
	}
	svcResult := s.deployService(ctx, app, revision, p, op.Op, before, activeStopGrace(active, p.name), s.journalCandidate(op, index+1))
	result.Services[p.name] = svcResult
	// A recorded candidate is authoritative while the step is unresolved: a
	// replacement that failed after creating its container must stay traceable
	// for reconciliation.
	if svcResult.After != "" {
		step.After = svcResult.After
	}
	fail := func(err error) error {
		step.State = domain.AppStepFailed
		step.Error = err.Error()
		step.Diagnostics = svcResult.Diagnostics
		op.Warnings = journalWarnings(collectCleanupWarnings(result.Services))
		op.Outcome = ComputeOutcome(result.Services)
		if saveErr := s.deps.State.SaveOperation(ctx, *op); saveErr != nil {
			log.Warn().Err(saveErr).Msg("deployment: failed to record service failure")
		}
		return err
	}
	if svcResult.Result == "failed" {
		// Fail fast: later services stay unchanged.
		return fail(fmt.Errorf("deployment: service %q failed at %s: %s: %w",
			p.name, stepID, svcResult.Error, domain.ErrAppStateConflict))
	}
	// Publish the per-service effective state: the new binds become the
	// service's recorded backend before the proxy is repointed at them.
	if err := s.publishService(ctx, app, revision, p, svcResult, op.Op); err != nil {
		svcResult.Result = "failed"
		svcResult.Error = err.Error()
		result.Services[p.name] = svcResult
		return fail(err)
	}
	// Rebuild and publish the traffic graph. ACTIVE already names the new
	// container, but the operation is a failure until routing accepted it,
	// so a rejected graph must never be journaled as success.
	if err := s.refreshTraffic(ctx, app); err != nil {
		svcResult.Result = "failed"
		svcResult.Error = err.Error()
		result.Services[p.name] = svcResult
		return fail(err)
	}
	result.Services[p.name] = svcResult
	step.State = domain.AppStepSucceeded
	step.Error = ""
	if saveErr := s.deps.State.SaveOperation(ctx, *op); saveErr != nil {
		log.Warn().Err(saveErr).Msg("deployment: failed to checkpoint service progress")
	}
	return nil
}

// failOperation records a terminal failure on an already claimed
// journal, so a keyed repeat replays a terminal outcome instead of
// reading a permanently in-flight claim. stepID names the failing phase
// (a service step, a traffic publication, or a bare error). Journal-write
// failures are logged, never returned: the caller already holds a more
// specific error, and the next mutation's recovery pass converges state.
func (s *Service) failOperation(ctx context.Context, op *domain.AppOperation, err error, stepID string) {
	op.Outcome = domain.AppOutcomeFailed
	op.Steps = append(op.Steps, domain.AppOperationStep{
		ID: stepID, State: domain.AppStepFailed, Error: err.Error(),
	})
	if saveErr := s.deps.State.SaveOperation(ctx, *op); saveErr != nil {
		log := zerowrap.FromCtx(ctx)
		log.Warn().Err(saveErr).Str("op", op.Op).Msg("deployment: failed to record operation failure")
	}
}

// failTrafficPublication records that the final graph apply was rejected.
// The workload steps already ran and stay accurate, but the operation is a
// failure: routing did not accept the published state, so a later replay
// of the same key must never read success.
func (s *Service) failTrafficPublication(ctx context.Context, op *domain.AppOperation, err error) {
	s.failOperation(ctx, op, err, "traffic.publish")
}

// journaledOrNil reports a preflight that already answered the request
// key: the stored journal when there is one, the bare error otherwise.
func journaledOrNil(input DeployInput, op *domain.AppOperation, err error) (*DeployResult, error) {
	if op == nil {
		return nil, err
	}
	return journaledDeployResult(input, op), err
}

func journaledDeployResult(input DeployInput, op *domain.AppOperation) *DeployResult {
	return &DeployResult{
		Op:       op.Op,
		App:      input.App,
		Revision: op.InputRevision,
		Outcome:  op.Outcome,
	}
}

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
// entry deleted. A failure leaves the remaining services untouched and
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
		delete(active.Services, name)
	}
	if len(names) == 0 {
		return nil, nil, nil, nil
	}
	if err := s.deps.State.SaveActive(ctx, active); err != nil {
		return steps, names, cleanupWarnings, fmt.Errorf("deployment: persist service removals: %w", err)
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
		retired := s.retireContainer(ctx, app, retireOptions{
			Service: name, Grace: serviceStopGrace(eff), ClearInhibition: true,
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

// candidateJournal durably records a freshly created replacement container
// before it can be started or join a network. An interrupted deployment then
// leaves a trace boot recovery removes, instead of an untracked running
// generation that could overlap the one recovery rebuilds.
//
// The window between the runtime creating a container and this write is the
// irreducible one: a container ID cannot be recorded before it exists.
// sync/atomic is not needed here: the journal write is synchronous.
type candidateJournal func(ctx context.Context, containerID string) error

// journalCandidate records one created candidate in the operation journal.
// The step is addressed by index and read at write time, so a later append to
// the operation's steps can never leave the recorder writing to a stale
// element. A nil operation (an internal path with no journal) records nothing.
func (s *Service) journalCandidate(op *domain.AppOperation, index int) candidateJournal {
	if op == nil || index < 0 || index >= len(op.Steps) {
		return nil
	}
	return func(ctx context.Context, containerID string) error {
		op.Steps[index].After = containerID
		if err := s.deps.State.SaveOperation(ctx, *op); err != nil {
			return fmt.Errorf("deployment: record candidate %s: %w", containerID, err)
		}
		return nil
	}
}

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
// Once the candidate is converged, the inhibition the replacement wrote for
// the superseded container is cleared. A step carrying both Before and After
// records that the superseded container was confirmed gone before the
// candidate was created (deployService retires it first), so the marker
// protects nothing and would otherwise refuse the boot/start/restart rebuild
// from ACTIVE.
func (s *Service) convergeCandidate(ctx context.Context, app string, active domain.AppActive, step *domain.AppOperationStep, terminal bool, log zerowrap.Logger) ([]CleanupWarning, error) {
	if publishedContainer(active, step.After) {
		// The published generation: keep it. A step that already succeeded is
		// left as recorded; a routing apply that never ran is republished by
		// the periodic recovery pass.
		if !terminal && step.State != domain.AppStepSucceeded {
			step.State = domain.AppStepFailed
			step.Error = "interrupted before traffic publication; the published container is kept and routing is republished"
		}
	} else {
		retired := s.retireContainer(ctx, app, retireOptions{Service: step.Service, Force: true}, step.After)
		if !retired.Gone {
			if !terminal {
				step.State = domain.AppStepFailed
				step.Error = "interrupted before publication; the unpublished candidate could not be removed: " + cleanupDetail(retired)
				log.Warn().Str("app", app).Str("container", step.After).Msg("deployment: unpublished candidate could not be removed")
			}
			return retired.Warnings, fmt.Errorf("deployment: candidate %s of service %q could not be removed: %s", step.After, step.Service, cleanupDetail(retired))
		}
		if !terminal {
			step.State = domain.AppStepFailed
			step.Error = "interrupted before publication; the unpublished candidate was removed"
		}
	}
	// The candidate is gone (or published): the replacement-pending
	// inhibition of the superseded container is stale. Dropping it lets
	// boot/start/restart rebuild that generation from ACTIVE; the single
	// writer is preserved because the superseded container was proven gone
	// before the candidate existed.
	if step.Service != "" && step.Before != "" && step.Before != step.After {
		if err := s.clearRecoveryInhibition(ctx, app, step.Service, step.Before); err != nil {
			log.Warn().Err(err).Str("app", app).Str("container", step.Before).Msg("deployment: clear stale replacement inhibition")
			return []CleanupWarning{{Service: step.Service, Leftover: step.Before, Detail: "clear recovery inhibition: " + err.Error()}}, nil
		}
	}
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

// deployService replaces one service sequentially and returns its terminal
// result. before is the ACTIVE container being superseded (empty on a first
// deployment); beforeGrace is its effective stop grace.
//
// The order is fixed: withdraw from traffic and confirm it, durably inhibit
// a single-writer generation, retire the superseded container and confirm
// it is gone, then create and start the replacement and wait for its
// readiness probe. Nothing is created before the previous generation is
// confirmed gone, so two generations never overlap; a withdrawal or
// retirement that cannot be confirmed aborts without creating a candidate.
func (s *Service) deployService(ctx context.Context, app, revision string, p pinnedService, opID, before string, beforeGrace time.Duration, journal candidateJournal) ServiceResult {
	// Revalidate the current authorization and source before withdrawing or
	// retiring the serving generation. A reload between preflight and this
	// service step must fail without mutating the existing workload.
	if _, err := s.resolveServiceBinds(app, p.spec); err != nil {
		return s.failResult(revision, before, "", err)
	}
	// Withdraw the service from traffic first and confirm it: a withdrawal
	// that cannot be applied must block every container mutation, so no
	// unverified generation stays routable. A first deployment has no
	// published generation to withdraw.
	if before != "" {
		if err := s.withdrawForRecovery(ctx, app, p.name); err != nil {
			return s.failResult(revision, before, "", err)
		}
	}
	// A volume- or bind-owning replacement may write while the old
	// generation still exists and could be revived by native restart policy.
	// Inhibit that generation durably BEFORE the write can happen, so boot or
	// periodic recovery can never restart the old writer on top of the new
	// one. Cleared once the safe generation is published (publishService) or
	// the operator removes the app.
	if before != "" && singleWriterRequired(p.spec) {
		if err := s.inhibitRecovery(ctx, app, p.name, before, domain.AppInhibitReplacementPending, opID); err != nil {
			return s.failResult(revision, before, "", err)
		}
	}
	if before != "" {
		// The superseded generation must be confirmed gone before a new
		// generation starts: a failed retirement aborts the replacement
		// instead of allowing overlapping generations, and the generation
		// keeps its inhibition and its claims.
		retired := s.retireContainer(ctx, app, retireOptions{
			Service: p.name, Grace: beforeGrace,
		}, before)
		if !retired.Gone {
			return s.failResult(revision, before, "", fmt.Errorf("deployment: retire superseded container %s: %s", before, cleanupDetail(retired)))
		}
	}
	created, binds, udpBinds, err := s.createAndStart(ctx, app, revision, p, opID, journal)
	if err != nil {
		result := s.failResult(revision, before, "", err)
		if created != nil {
			// The candidate exists: keep its ID in the terminal result so the
			// journal and the operator can still trace it.
			result.After = created.ID
		}
		return result
	}
	if err := s.waitServiceReady(ctx, app, created.ID, deploymentReadiness(p.spec), binds); err != nil {
		// Capture redacted diagnostics while the failed replacement still
		// exists, then remove only that candidate. The superseded container
		// is already gone and is never recreated. Cleanup is best effort: a
		// canceled context can leave the candidate behind, which is then
		// reported as a leftover warning instead of a false success.
		tail := s.redactDiagnostics(ctx, app, p, s.logTail(ctx, created.ID))
		cleanup := s.retireCandidate(ctx, app, p.name, created.ID)
		return ServiceResult{
			Result:            "failed",
			EffectiveRevision: revision,
			Before:            before,
			After:             created.ID,
			RestartUnsafe:     singleWriterRequired(p.spec),
			Error:             err.Error(),
			Diagnostics:       tail,
			CleanupWarnings:   cleanup,
		}
	}
	return ServiceResult{
		Result:            "deployed",
		EffectiveRevision: revision,
		Before:            before,
		After:             created.ID,
		RestartUnsafe:     singleWriterRequired(p.spec),
		BackendBinds:      binds,
		UDPBackendBinds:   udpBinds,
	}
}

// singleWriterRequired reports services that must never have two generations
// running concurrently. Persistent volumes and any bind (especially a
// writable one) may be written by both, so they are treated identically.
func singleWriterRequired(spec domain.AppService) bool {
	return len(spec.Volumes) > 0 || len(spec.Binds) > 0
}

// refreshTraffic rebuilds the proxy host index and applies the full
// HTTP/L4 graph after activation. A failure is fatal to the caller:
// ACTIVE is published but the new service is not routable, so the
// operation must not report success. Any service of this app whose
// fail-closed withdrawal was never applied is withdrawn again first, so
// a later publication never runs on top of unproven forwarding.
func (s *Service) refreshTraffic(ctx context.Context, app string) error {
	if s.deps.Traffic == nil {
		return nil
	}
	for _, service := range s.publication.pending(app) {
		if err := s.withdrawForRecovery(ctx, app, service); err != nil {
			return err
		}
	}
	if err := s.deps.Traffic.RebuildTraffic(ctx); err != nil {
		return fmt.Errorf("deployment: rebuild traffic for %q: %w", app, err)
	}
	return nil
}

// reserveVolumeOwnership durably records one app-owned volume as
// attached BEFORE the runtime volume is created, so a crash between the
// two leaves a protected record instead of an unowned volume. The
// caller passes the validated incarnation ID from ensureIncarnationID,
// which already assigned it, so no reload is needed for the labels.
func (s *Service) reserveVolumeOwnership(ctx context.Context, app, appID, service, volumeName, runtimeName string) error {
	ownership, err := s.deps.State.LoadOwnership(ctx, app)
	if err != nil {
		return fmt.Errorf("deployment: load ownership: %w", err)
	}
	if ownership.App == "" {
		ownership.App = app
	}
	if ownership.ID == "" {
		ownership.ID = appID
	}
	entry := domain.AppOwnedVolume{
		Name:        volumeName,
		Service:     service,
		RuntimeName: runtimeName,
		State:       domain.AppResourceAttached,
	}
	replaced := false
	for i, existing := range ownership.Volumes {
		if existing.RuntimeName == runtimeName {
			ownership.Volumes[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		ownership.Volumes = append(ownership.Volumes, entry)
	}
	if err := s.deps.State.SaveOwnership(ctx, ownership); err != nil {
		return fmt.Errorf("deployment: reserve volume ownership: %w", err)
	}
	return nil
}

// volumeProvenanceLabels is the label set stamped on an app-created
// volume. Ownership is never inferred from the volume name, so the
// labels must carry the full provenance: app, incarnation UUID, and
// service. The managed marker is stamped here as well as merged by the
// adapter, so provenance never depends on adapter behaviour alone.
func volumeProvenanceLabels(app, appID, service, revision string) map[string]string {
	labels := map[string]string{
		domain.LabelManaged:     "true",
		domain.LabelApp:         app,
		domain.LabelAppService:  service,
		domain.LabelAppRevision: revision,
	}
	if appID != "" {
		labels[domain.LabelAppID] = appID
	}
	return labels
}

// createAndStart builds the runtime config and starts the replacement.
// Preflight already pulled and inspected the pinned runtime image.
// Every TCP-capable interface port is published on 127.0.0.1 ephemeral
// plus every UDP interface port on 127.0.0.1/udp ephemeral: readiness
// and the proxy dial these loopback binds rootless-first,
// never container IPs. The runtime keeps native restarts; Gordon
// reconciles intent at boot and in the monitor (accepted decision:
// native runtime restarts plus daemon reconciliation).
func (s *Service) createAndStart(ctx context.Context, app, revision string, p pinnedService, opID string, journal candidateJournal) (*domain.Container, map[int]int, map[int]int, error) {
	// Re-resolve binds from the current policy immediately before any
	// runtime mutation: a bind revoked since preflight must fail here,
	// before volume ownership or container creation.
	resolvedBinds, err := s.resolveServiceBinds(app, p.spec)
	if err != nil {
		return nil, nil, nil, err
	}
	env, err := s.serviceEnv(ctx, app, p)
	if err != nil {
		return nil, nil, nil, err
	}
	image := runtimeImageRef(p)
	// The incarnation network is verified or created before any
	// container exists, so a workload is only ever started on a
	// Gordon-owned network that no other app shares.
	appID, err := s.ensureIncarnationID(ctx, app)
	if err != nil {
		return nil, nil, nil, err
	}
	nets := s.resolveAppNetworks(appID, p.sharedNetworks)
	if err := s.ensureAppNetworks(ctx, app, appID, nets); err != nil {
		return nil, nil, nil, err
	}
	volumes := map[string]string{}
	readOnlyVolumes := map[string]string{}
	for _, vol := range p.spec.Volumes {
		runtimeName := domain.RuntimeVolumeName(app, p.spec.Name, vol.Name)
		// Durable ownership is reserved BEFORE the runtime volume
		// exists: a crash in between leaves a protected record, never
		// an unowned volume that prune could later adopt. The
		// incarnation ID is already validated above, so the labels
		// carry the same ID without another ownership load.
		if err := s.reserveVolumeOwnership(ctx, app, appID, p.spec.Name, vol.Name, runtimeName); err != nil {
			return nil, nil, nil, err
		}
		if err := s.deps.Runtime.CreateVolume(ctx, runtimeName, volumeProvenanceLabels(app, appID, p.spec.Name, revision)); err != nil {
			// CreateVolume is idempotent at the adapter; existence was
			// checked at preflight, so only real backend errors fail here.
			return nil, nil, nil, fmt.Errorf("deployment: create volume %q: %w", vol.Name, err)
		}
		// A declared read-only mount must reach the runtime as read-only:
		// a writable mount would let the service modify protected data.
		if vol.ReadOnly {
			readOnlyVolumes[vol.Path] = runtimeName
			continue
		}
		volumes[vol.Path] = runtimeName
	}
	config := &domain.ContainerConfig{
		Image:           image,
		Name:            domain.LogicalServiceIdentity(app, p.spec.Name) + "--" + shortOp(opID),
		Env:             env,
		Entrypoint:      append([]string(nil), p.spec.Command...),
		Volumes:         volumes,
		ReadOnlyVolumes: readOnlyVolumes,
		Binds:           resolvedBinds,
		Labels:          appLabels(app, p.spec.Name, revision),
		AutoRemove:      false,
		RestartPolicy:   domain.RestartPolicyAlways,
		PortPublishes:   backendPublishes(p.spec),
		NetworkMode:     nets.private,
		Hostname:        p.spec.Name,
		Aliases:         []string{p.spec.Name},
		MemoryLimit:     s.deps.Limits.MemoryBytes,
		NanoCPUs:        s.deps.Limits.NanoCPUs,
		PidsLimit:       s.deps.Limits.PidsLimit,
	}
	created, err := s.createContainer(ctx, p.name, config)
	if err != nil {
		return nil, nil, nil, err
	}
	// The candidate ID is durably journaled before the container is started
	// or joins a shared network, so an interrupted deployment can be cleaned
	// up instead of leaving an untracked running generation.
	if err := s.recordCandidate(ctx, app, p.spec.Name, created.ID, journal); err != nil {
		return nil, nil, nil, err
	}
	if err := s.connectSharedNetworks(ctx, created.ID, nets); err != nil {
		s.retireCandidate(ctx, app, p.spec.Name, created.ID)
		return created, nil, nil, err
	}
	if err := s.deps.Runtime.StartContainer(ctx, created.ID); err != nil {
		s.retireCandidate(ctx, app, p.spec.Name, created.ID)
		return created, nil, nil, fmt.Errorf("deployment: start container: %w", err)
	}
	binds, udpBinds, err := s.readBackendBinds(ctx, app, p.spec.Name, created.ID, backendPorts(p.spec))
	if err != nil {
		s.retireCandidate(ctx, app, p.spec.Name, created.ID)
		return created, nil, nil, err
	}
	return created, binds, udpBinds, nil
}

// runtimeImageRef resolves the exact runtime image reference of one pinned
// service: the image preflight resolved, else the manifest reference pinned to
// its digest.
func runtimeImageRef(p pinnedService) string {
	if p.runtimeImage != "" {
		return p.runtimeImage
	}
	if p.digest == "" {
		return p.spec.Image
	}
	return stripImageTag(p.spec.Image) + "@" + p.digest
}

// recordCandidate durably journals a freshly created candidate before it is
// started. A candidate whose ID cannot be recorded must not keep running:
// recovery could never find it, so it is removed and the failure surfaced.
func (s *Service) recordCandidate(ctx context.Context, app, service, containerID string, journal candidateJournal) error {
	if journal == nil {
		return nil
	}
	if err := journal(ctx, containerID); err != nil {
		retired := s.retireContainer(ctx, app, retireOptions{Service: service, Force: true}, containerID)
		if !retired.Gone {
			return fmt.Errorf("%w (candidate cleanup: %s)", err, cleanupDetail(retired))
		}
		return err
	}
	return nil
}

func (s *Service) createContainer(ctx context.Context, service string, config *domain.ContainerConfig) (*domain.Container, error) {
	created, err := s.deps.Runtime.CreateContainer(ctx, config)
	if err == nil {
		return created, nil
	}
	if len(config.Binds) > 0 {
		// A CreateContainer failure is a runtime error, not a bind policy
		// violation: policy was already enforced by resolveServiceBinds
		// before this call. Runtime errors may embed the resolved host
		// path, so redact the whole cause instead of mislabelling it and
		// keep it out of operation journals and API/CLI responses.
		return nil, fmt.Errorf("deployment: create container for service %q with administrative mounts: runtime error redacted", service)
	}
	return nil, fmt.Errorf("deployment: create container: %w", err)
}

// backendPorts collects every interface container port for loopback
// publication: public HTTP + TCP interfaces on tcp plus UDP interfaces on
// udp, plus an explicit TCP readiness port. Internal HTTP ports are
// excluded: they are reached only over the private network and must keep
// no host binding. Each publish is on 127.0.0.1 ephemeral (never public).
// Deduplicated by (protocol, container port).
func backendPorts(spec domain.AppService) []domain.ContainerBackendPort {
	seen := map[domain.ContainerBackendPort]struct{}{}
	var ports []domain.ContainerBackendPort
	add := func(port int, protocol domain.NetworkProtocol) {
		if port <= 0 {
			return
		}
		key := domain.ContainerBackendPort{ContainerPort: port, Protocol: protocol}
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			ports = append(ports, key)
		}
	}
	for _, h := range spec.HTTP {
		if !h.IsPublic() {
			continue
		}
		add(h.Port, domain.NetworkProtocolTCP)
	}
	for _, t := range spec.TCP {
		add(t.Port, domain.NetworkProtocolTCP)
	}
	for _, u := range spec.UDP {
		add(u.Port, domain.NetworkProtocolUDP)
	}
	// Readiness probes are TCP-only (validation rejects UDP-only
	// services with tcp/http readiness); the readiness port matches a
	// declared TCP container port. An internal-only port is never
	// published, so readiness metadata cannot create a host binding for
	// it.
	if spec.Readiness.Port > 0 && !spec.InternallyOnlyPort(spec.Readiness.Port) {
		add(spec.Readiness.Port, domain.NetworkProtocolTCP)
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Protocol != ports[j].Protocol {
			return ports[i].Protocol < ports[j].Protocol
		}
		return ports[i].ContainerPort < ports[j].ContainerPort
	})
	return ports
}

// backendPublishes maps backend ports to 127.0.0.1 ephemeral publishes.
func backendPublishes(spec domain.AppService) []domain.ContainerPortPublish {
	ports := backendPorts(spec)
	publishes := make([]domain.ContainerPortPublish, 0, len(ports))
	for _, port := range ports {
		publishes = append(publishes, domain.ContainerPortPublish{
			HostIP:        "127.0.0.1",
			HostPort:      0,
			ContainerPort: port.ContainerPort,
			Protocol:      port.Protocol,
		})
	}
	return publishes
}

// readBackendBinds resolves each published container port to its
// 127.0.0.1 host port and registers the Gordon-generated claims
// (owner gordon-backend) in the global checkpoint. Missing binds fail:
// an unbound backend can serve neither readiness nor proxy traffic.
// A registration conflict removes the CANDIDATE container and fails —
// callers must only pass freshly created candidates here, never an
// existing ACTIVE container (see inspectBackendBinds).
func (s *Service) readBackendBinds(ctx context.Context, app, service, containerID string, ports []domain.ContainerBackendPort) (map[int]int, map[int]int, error) {
	if len(ports) == 0 {
		return map[int]int{}, map[int]int{}, nil
	}
	observed, err := s.deps.Runtime.GetContainerBackendBinds(ctx, containerID, ports)
	if err != nil {
		return nil, nil, fmt.Errorf("deployment: backend binds: %w", err)
	}
	binds, udpBinds := splitBackendBinds(observed)
	claims := backendClaims(app, service, containerID, observed)
	if len(claims) == 0 {
		return binds, udpBinds, nil
	}
	if err := s.deps.State.RegisterBackendBinds(ctx, claims); err != nil {
		s.retireCandidate(ctx, app, service, containerID)
		return nil, nil, err
	}
	return binds, udpBinds, nil
}

// inspectBackendBinds re-inspects the loopback publishes of an EXISTING
// container and registers the Gordon-generated claims. Unlike
// readBackendBinds it never stops or removes the container: a
// registration conflict fails without touching the ACTIVE workload.
// Missing binds fail: an unbound backend must not stay routable.
func (s *Service) inspectBackendBinds(ctx context.Context, app, service, containerID string, ports []domain.ContainerBackendPort) (map[int]int, map[int]int, error) {
	if len(ports) == 0 {
		return map[int]int{}, map[int]int{}, nil
	}
	observed, err := s.deps.Runtime.GetContainerBackendBinds(ctx, containerID, ports)
	if err != nil {
		return nil, nil, fmt.Errorf("deployment: backend binds: %w", err)
	}
	binds, udpBinds := splitBackendBinds(observed)
	claims := backendClaims(app, service, containerID, observed)
	if len(claims) == 0 {
		return binds, udpBinds, nil
	}
	if err := s.deps.State.RegisterBackendBinds(ctx, claims); err != nil {
		return nil, nil, err
	}
	return binds, udpBinds, nil
}

// splitBackendBinds partitions observed binds by protocol. TCP feeds
// readiness and HTTP/TCP proxying; UDP feeds UDP relaying.
func splitBackendBinds(observed []domain.ContainerBackendBind) (map[int]int, map[int]int) {
	binds := map[int]int{}
	udpBinds := map[int]int{}
	for _, bind := range observed {
		if bind.Protocol == domain.NetworkProtocolUDP {
			udpBinds[bind.ContainerPort] = bind.HostPort
		} else {
			binds[bind.ContainerPort] = bind.HostPort
		}
	}
	return binds, udpBinds
}

// backendClaims maps observed binds to Gordon-generated loopback claims
// with the exact protocol (tcp/udp) for the global checkpoint.
func backendClaims(app, service, containerID string, observed []domain.ContainerBackendBind) []domain.AppListenerReservation {
	claims := make([]domain.AppListenerReservation, 0, len(observed))
	for _, bind := range observed {
		claims = append(claims, domain.AppListenerReservation{
			Proto:       string(bind.Protocol),
			IP:          "127.0.0.1",
			Port:        bind.HostPort,
			Service:     service,
			App:         app,
			Owner:       domain.OwnerGordonBackend,
			ContainerID: containerID,
		})
	}
	return claims
}

// stripImageTag removes any tag or digest suffix from an image reference.
// colon is a registry port (localhost:15500/e2e/web:v1 strips to
// localhost:15500/e2e/web).
func stripImageTag(ref string) string {
	if base, _, ok := strings.Cut(ref, "@"); ok {
		return base
	}
	lastSlash := strings.LastIndex(ref, "/")
	if idx := strings.LastIndex(ref, ":"); idx > lastSlash {
		return ref[:idx]
	}
	return ref
}

// pullImage fetches the pinned image from the installation registry.
// External refs pull anonymously; installation-registry refs use the
// internal credentials. A missing registry config disables pulling
// (tests and pre-pulled environments).
func (s *Service) pullImage(ctx context.Context, image string) (string, error) {
	if err := s.validateImageSource(image, ""); err != nil {
		return "", err
	}
	if s.deps.Registry.Domain == "" {
		return image, nil
	}
	if !s.deps.ImagePolicy.IsInstallationImage(image) {
		if err := s.deps.Runtime.PullImage(ctx, image); err != nil {
			return "", fmt.Errorf("deployment: pull image %q: %w", image, err)
		}
		return image, nil
	}
	_, remainder, ok := strings.Cut(image, "/")
	if !ok || !strings.Contains(remainder, "@sha256:") {
		return "", fmt.Errorf("deployment: installation image must include an exact sha256 digest: %w", domain.ErrAppImageNotAllowed)
	}
	pullImage := s.deps.Registry.PullAddress + "/" + remainder
	request := domain.ImagePullRequest{Reference: pullImage, Username: s.deps.Registry.Username, Password: s.deps.Registry.Password, Transport: domain.ImagePullTransportHTTP}
	if err := s.deps.Runtime.PullImageWithOptions(ctx, request); err != nil {
		return "", fmt.Errorf("deployment: pull installation image %q via configured local HTTP transport %q: %w", image, s.deps.Registry.PullAddress, err)
	}
	digest := remainder[strings.LastIndex(remainder, "@")+1:]
	if err := s.deps.Runtime.VerifyImageDigest(ctx, pullImage, digest); err != nil {
		return "", fmt.Errorf("deployment: verify pulled installation image %q: %w", image, err)
	}
	return pullImage, nil
}

// serviceEnv resolves the service environment: the captured revision's
// app-wide public env on every service, plus that service's own secret
// values (memory only, UUID-keyed paths). Keys are emitted sorted and
// deterministic. Overlapping public/secret keys are invalid with no
// precedence; preflight rejects them before any mutation.
func (s *Service) serviceEnv(ctx context.Context, app string, p pinnedService) ([]string, error) {
	spec := p.spec
	envMap := make(map[string]string, len(p.appEnv)+len(spec.Secrets))
	for key, value := range p.appEnv {
		envMap[key] = value
	}
	keys := make([]string, 0, len(spec.Secrets))
	for envKey := range spec.Secrets {
		keys = append(keys, envKey)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return sortedEnv(envMap), nil
	}
	id, err := s.appSecretID(ctx, app)
	if err != nil {
		return nil, err
	}
	for _, envKey := range keys {
		if _, overlap := envMap[envKey]; overlap {
			return nil, fmt.Errorf("deployment: env key %q collides with a secret key in service %q: %w", envKey, spec.Name, domain.ErrInvalidAppSpec)
		}
		path := domain.AppSecretPathForID(id, app, spec.Name, spec.Secrets[envKey])
		value, err := s.deps.Secrets.GetSecret(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("deployment: secret %s (env %s) missing: %w", path, envKey, domain.ErrAppSecretMissing)
		}
		envMap[envKey] = value
	}
	return sortedEnv(envMap), nil
}

// sortedEnv renders a KEY=value list in sorted key order. Values stay in
// memory for container creation only; nothing is persisted or logged.
func sortedEnv(envMap map[string]string) []string {
	if len(envMap) == 0 {
		return nil
	}
	keys := make([]string, 0, len(envMap))
	for key := range envMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+envMap[key])
	}
	return env
}

// publishService writes the per-service effective record once the
// replacement passed readiness.
func (s *Service) publishService(ctx context.Context, app, revision string, p pinnedService, result ServiceResult, opID string) error {
	if app == "" {
		return fmt.Errorf("deployment: publish active: empty app: %w", domain.ErrAppStateConflict)
	}
	active, ok, err := s.deps.State.LoadActive(ctx, app)
	if err != nil {
		return fmt.Errorf("deployment: load active: %w", err)
	}
	if !ok {
		active = domain.AppActive{App: app, Services: map[string]domain.AppEffectiveService{}}
	}
	if active.Services == nil {
		active.Services = map[string]domain.AppEffectiveService{}
	}
	active.Services[p.name] = domain.AppEffectiveService{
		EffectiveRevision: revision,
		ActivatedBy:       opID,
		ActivatedAt:       time.Now().UTC(),
		Image:             p.spec.Image,
		Digest:            p.digest,
		Container:         result.After,
		Spec:              p.spec,
		BackendBinds:      result.BackendBinds,
		UDPBackendBinds:   result.UDPBackendBinds,
	}
	active.ConvergedRevision = revision
	converged := true
	for _, svc := range active.Services {
		if svc.EffectiveRevision != revision {
			converged = false
			break
		}
	}
	active.Converged = converged
	if converged {
		active.Networks = append([]domain.AppSharedNetwork(nil), p.appNetworks...)
	}
	if err := s.deps.State.SaveActive(ctx, active); err != nil {
		return fmt.Errorf("deployment: publish active: %w", err)
	}
	if err := s.recordOwnership(ctx, app, p); err != nil {
		return err
	}
	// The superseded generation is now safely replaced: clearing its
	// inhibition is what makes this generation authoritative. A failed
	// publication leaves the marker in place.
	if result.Before != "" && result.Before != result.After {
		if err := s.clearRecoveryInhibition(ctx, app, p.name, result.Before); err != nil {
			return err
		}
	}
	return nil
}

// recordOwnership stamps volume/secret/network ownership after publication.
func (s *Service) recordOwnership(ctx context.Context, app string, p pinnedService) error {
	ownership, err := s.deps.State.LoadOwnership(ctx, app)
	if err != nil {
		return err
	}
	if ownership.App == "" {
		ownership.App = app
	}
	// ownership.ID arrives from LoadOwnership (store-assigned UUID);
	// empty only for legacy records predating UUIDs.
	volumes := append([]domain.AppOwnedVolume(nil), ownership.Volumes...)
	seen := map[string]int{}
	for i, vol := range volumes {
		seen[vol.Name] = i
	}
	for _, vol := range p.spec.Volumes {
		entry := domain.AppOwnedVolume{
			Name:        vol.Name,
			Service:     p.spec.Name,
			RuntimeName: domain.RuntimeVolumeName(app, p.spec.Name, vol.Name),
			State:       domain.AppResourceAttached,
		}
		if idx, ok := seen[vol.Name]; ok {
			volumes[idx] = entry
		} else {
			seen[vol.Name] = len(volumes)
			volumes = append(volumes, entry)
		}
	}
	ownership.Volumes = volumes
	secrets := append([]domain.AppOwnedSecret(nil), ownership.Secrets...)
	secretSeen := map[string]int{}
	for i, secret := range secrets {
		secretSeen[secret.Service+"\x00"+secret.Env] = i
	}
	for envKey, name := range p.spec.Secrets {
		entry := domain.AppOwnedSecret{
			Service: p.spec.Name,
			Env:     envKey,
			Name:    name,
			Path:    domain.AppSecretPathForID(ownership.ID, app, p.spec.Name, name),
			State:   domain.AppResourceAttached,
		}
		key := p.spec.Name + "\x00" + envKey
		if idx, ok := secretSeen[key]; ok {
			secrets[idx] = entry
		} else {
			secretSeen[key] = len(secrets)
			secrets = append(secrets, entry)
		}
	}
	ownership.Secrets = secrets
	ownership.Images = recordImageOwnership(ownership.Images, p)
	recordNetworkOwnership(&ownership, s.deps.Networks.Prefix, p.sharedNetworks)
	if ownership.Services == nil {
		ownership.Services = map[string]domain.AppServiceRecovery{}
	}
	ownership.Services[p.spec.Name] = domain.AppServiceRecovery{RestartUnsafe: singleWriterRequired(p.spec)}
	if err := s.deps.State.SaveOwnership(ctx, ownership); err != nil {
		return fmt.Errorf("deployment: record ownership: %w", err)
	}
	return nil
}

// recordImageOwnership stamps one service's pinned image into the app's
// owned-image list. Positive durable ownership is what later authorizes
// runtime image prune; image labels alone never do.
func recordImageOwnership(existing []domain.AppOwnedImage, p pinnedService) []domain.AppOwnedImage {
	entry := domain.AppOwnedImage{
		Service:   p.spec.Name,
		Reference: p.spec.Image,
		Digest:    p.digest,
		State:     domain.AppResourceAttached,
	}
	out := append([]domain.AppOwnedImage(nil), existing...)
	for i, image := range out {
		if image.Service == entry.Service {
			out[i] = entry
			return out
		}
	}
	return append(out, entry)
}

// failResult builds a preflight/create failure before any container exists.
func (s *Service) failResult(revision, before, after string, err error) ServiceResult {
	return ServiceResult{
		Result:            "failed",
		EffectiveRevision: revision,
		Before:            before,
		After:             after,
		Error:             err.Error(),
	}
}

// logTail returns the bounded recent log tail of the failing container.
// The read is bounded by the caller's deadline and by an internal cap so
// a slow or hung log stream cannot stall the operation.
func (s *Service) logTail(ctx context.Context, containerID string) []string {
	if containerID == "" || ctx.Err() != nil {
		return nil
	}
	tailCtx, cancel := context.WithTimeout(ctx, failLogTailTimeout)
	defer cancel()
	stream, err := s.deps.Runtime.GetContainerLogs(tailCtx, containerID, false)
	if err != nil {
		return nil
	}
	defer func() { _ = stream.Close() }()
	lines, err := tailLines(stream, failLogTailLines)
	if err != nil {
		return nil
	}
	return lines
}

// redactDiagnostics replaces every value of this service's secrets in the
// given log lines. Secret values are read into memory only. When a secret
// cannot be read, diagnostics are dropped entirely: unredacted application
// output is never persisted.
func (s *Service) redactDiagnostics(ctx context.Context, app string, p pinnedService, lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	if len(p.spec.Secrets) == 0 || s.deps.Secrets == nil {
		return lines
	}
	id, err := s.appSecretID(ctx, app)
	if err != nil {
		return nil
	}
	redacted := append([]string(nil), lines...)
	for _, name := range p.spec.Secrets {
		path := domain.AppSecretPathForID(id, app, p.spec.Name, name)
		value, err := s.deps.Secrets.GetSecret(ctx, path)
		if err != nil || value == "" {
			return nil
		}
		for i, line := range redacted {
			redacted[i] = strings.ReplaceAll(line, value, "[redacted]")
		}
	}
	return redacted
}

// tailLines keeps the last N lines; logs are untrusted app output.
func tailLines(r io.Reader, n int) ([]string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// deploymentReadiness resolves the readiness check a deploy or restart
// runs. Readiness never depends on how a service is replaced: a declared
// http, tcp, or log check is used as declared, and an undeclared check
// ("none" or unset) keeps the implicit HTTP probe on a service Gordon can
// probe over the published loopback bind — at least one effective-public
// HTTP interface, no L4 interface, no volume, and no bind. An undeclared
// probe on a stateful or L4 workload stays unchecked instead: an HTTP GET on
// the published port proves nothing there, so Gordon never guesses one.
func deploymentReadiness(spec domain.AppService) domain.AppService {
	if !implicitHTTPProbe(spec) {
		return spec
	}
	spec.Readiness.Type = domain.AppReadinessHTTP
	return spec
}

// implicitHTTPProbe reports whether a service that declares no readiness
// check receives the implicit HTTP probe when a deploy or restart starts it.
// Recovery verification of an already running generation keeps the declared
// readiness unchanged: it re-checks a generation it did not replace.
func implicitHTTPProbe(spec domain.AppService) bool {
	if spec.Readiness.Type != "" && spec.Readiness.Type != domain.AppReadinessNone {
		return false
	}
	if len(spec.HTTP) == 0 || len(spec.TCP) > 0 || len(spec.UDP) > 0 || len(spec.Volumes) > 0 || len(spec.Binds) > 0 {
		return false
	}
	for _, h := range spec.HTTP {
		if !h.IsPublic() {
			return false
		}
	}
	return true
}

// appLabels stamps engine ownership on every created container.
func appLabels(app, service, revision string) map[string]string {
	return map[string]string{
		domain.LabelManaged:     "true",
		domain.LabelApp:         app,
		domain.LabelAppService:  service,
		domain.LabelAppRevision: revision,
	}
}

// shortOp keeps container names short but unique per op.
func shortOp(opID string) string {
	trimmed := strings.TrimPrefix(opID, "op-")
	if len(trimmed) > 12 {
		trimmed = trimmed[:12]
	}
	if trimmed == "" {
		return "op"
	}
	return trimmed
}
