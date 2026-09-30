package deployment

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"sort"
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

// AbandonDeploy settles a claim this process owns but will never execute, such
// as when daemon shutdown races the hand-off between StartDeploy and
// ExecuteDeploy. It drops the in-process live marker and converges the still
// non-terminal journal as an interrupted operation, so no durable claim stays
// marked live forever and a later reconciliation reads a terminal outcome
// instead of a permanently in-flight key. It takes the app lock but performs
// no workload effect.
func (s *Service) AbandonDeploy(ctx context.Context, claim DeployClaim) error {
	if claim.App == "" || claim.Op == "" {
		return fmt.Errorf("deployment: abandon requires an app and operation identity: %w", domain.ErrAppStateConflict)
	}
	// The marker is dropped before any fallible step: even if convergence
	// fails, the claim is no longer owned here, so a later reconciliation
	// (foreground or boot) can converge it instead of skipping it as live.
	s.clearOperationLive(claim.App, claim.Op)
	release, err := s.coord.acquire(ctx, claim.App)
	if err != nil {
		return err
	}
	defer release()
	return s.reconcileInterruptedDeploy(ctx, claim.App)
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
	// The claimed revision is authoritative for the whole execution: it was
	// resolved when the key was claimed and is what the journal records, so a
	// desired-state change between the two phases must never redirect this
	// operation to a different revision than the one it pinned.
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
	var svcResult ServiceResult
	if current, ok := s.unchangedService(ctx, app, p, active); ok {
		// Same image, spec, environment, and secret values already running:
		// keep the container.
		svcResult = ServiceResult{
			Result: ServiceResultUnchanged, EffectiveRevision: revision,
			Before: before, After: before,
			BackendBinds: current.BackendBinds, UDPBackendBinds: current.UDPBackendBinds,
			RestartUnsafe: singleWriterRequired(p.spec),
		}
		step.Detail = domain.AppServiceUnchanged + ": already running " + p.spec.Image + " (" + p.digest + ")"
	} else {
		svcResult = s.deployService(ctx, app, revision, p, op.Op, before, activeStopGrace(active, p.name), s.journalCandidate(op, index+1))
	}
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

// unchangedService reports whether the service already runs exactly what p
// would create, in a healthy published state: the same digest, service spec,
// app environment, and shared networks, in a running container that is not
// withdrawn or recovery-inhibited and whose recorded binds cover every
// backend port, and whose environment already holds the current secret
// values. Services with host binds or devices are never skipped: their
// resolved sources depend on host policy that ACTIVE does not record.
func (s *Service) unchangedService(ctx context.Context, app string, p pinnedService, active domain.AppActive) (domain.AppEffectiveService, bool) {
	eff, ok := active.Services[p.name]
	if !ok || eff.Container == "" || eff.Digest == "" || eff.Digest != p.digest {
		return eff, false
	}
	reason := s.unchangedRefusal(ctx, app, p, eff)
	if reason != "" {
		log := zerowrap.FromCtx(ctx)
		log.Debug().Str("app", app).Str("service", p.name).Str("reason", reason).
			Msg("deployment: same digest, replacing service")
		return eff, false
	}
	return eff, true
}

// unchangedRefusal returns why a service running the same digest must still
// be replaced, or "" when it can be kept.
func (s *Service) unchangedRefusal(ctx context.Context, app string, p pinnedService, eff domain.AppEffectiveService) string {
	switch {
	case len(p.spec.Binds) > 0 || len(p.spec.Devices) > 0:
		return "host binds or devices"
	case !domain.SameAppService(p.spec, eff.Spec):
		return "spec changed"
	case !bindsCover(eff, backendPorts(p.spec)):
		return "backend binds missing"
	case s.publication.inhibited(app, p.name):
		return "withdrawal pending"
	}
	previous, err := s.deps.State.LoadRevision(ctx, app, eff.EffectiveRevision)
	if err != nil {
		return "load effective revision: " + err.Error()
	}
	if !maps.Equal(previous.Spec.Env, p.appEnv) {
		return "app env changed"
	}
	if !reflect.DeepEqual(domain.AppServiceSharedNetworks(previous.Spec, p.name), p.sharedNetworks) {
		return "shared networks changed"
	}
	if inhibited, err := s.recoveryInhibited(ctx, app, p.name, eff.Container); err != nil || inhibited {
		return "recovery inhibited"
	}
	container, err := s.deps.Runtime.InspectContainer(ctx, eff.Container)
	if err != nil || container.Status != string(domain.ContainerStatusRunning) {
		return "container not running"
	}
	if !s.envCurrent(ctx, app, p, container.Env) {
		return "environment or secrets changed"
	}
	return ""
}

// envCurrent reports whether the running container was created with the
// environment p would get now, including current secret values. The
// runtime env also holds image-declared keys, so every desired entry must
// be present; removed keys are caught by the spec and app env comparisons.
// Values are compared in memory only and never logged.
func (s *Service) envCurrent(ctx context.Context, app string, p pinnedService, running []string) bool {
	desired, err := s.serviceEnv(ctx, app, p)
	if err != nil {
		return false
	}
	have := make(map[string]struct{}, len(running))
	for _, entry := range running {
		have[entry] = struct{}{}
	}
	for _, entry := range desired {
		if _, ok := have[entry]; !ok {
			return false
		}
	}
	return true
}

// bindsCover reports whether the recorded loopback binds publish every
// backend port the spec requires, so a kept container stays routable.
func bindsCover(eff domain.AppEffectiveService, ports []domain.ContainerBackendPort) bool {
	for _, port := range ports {
		binds := eff.BackendBinds
		if port.Protocol == domain.NetworkProtocolUDP {
			binds = eff.UDPBackendBinds
		}
		if binds[port.ContainerPort] == 0 {
			return false
		}
	}
	return true
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
	if _, err := s.resolveServiceDevices(app, p.spec); err != nil {
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
	candidate, err := s.createAndStart(ctx, app, revision, p, opID, journal)
	if err != nil {
		result := s.failResult(revision, before, "", err)
		if candidate.Container != nil {
			// The candidate exists: keep its ID in the terminal result so the
			// journal and the operator can still trace it.
			result.After = candidate.Container.ID
		}
		return result
	}
	created := candidate.Container
	if err := s.waitServiceReady(ctx, app, created.ID, deploymentReadiness(p.spec), candidate.TCPBinds); err != nil {
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
		BackendBinds:      candidate.TCPBinds,
		UDPBackendBinds:   candidate.UDPBinds,
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
	activatedBy, activatedAt := opID, time.Now().UTC()
	if previous, ok := active.Services[p.name]; ok && result.Result == ServiceResultUnchanged {
		// A kept container keeps the activation of the operation that created it.
		activatedBy, activatedAt = previous.ActivatedBy, previous.ActivatedAt
	}
	active.Services[p.name] = domain.AppEffectiveService{
		EffectiveRevision: revision,
		ActivatedBy:       activatedBy,
		ActivatedAt:       activatedAt,
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
