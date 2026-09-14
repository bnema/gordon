package apps

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

// AppServiceImpl implements in.AppService over the deployment engine. The
// daemon owns this implementation; CLI always reaches it via the daemon.
// All mutations run recovery-before-mutation through the engine.
type AppServiceImpl struct {
	store   out.AppState
	deploy  deployEngine
	secrets out.SecretWriter
	log     zerowrap.Logger
	// core is the single configured apps service. It is built once and
	// reconfigured in place, never reconstructed per request.
	core *Service

	// lifecycleMu guards the daemon lifecycle. daemonCtx is the
	// daemon-owned parent for background executions: it is never a request
	// context, so request cancellation cannot abort an in-flight deploy.
	// stopped gates new executions once Shutdown runs, handoffs tracks the
	// Deploy request hand-offs (claim through scheduling or settling) so
	// Shutdown never closes state under a claim it did not wait for, and
	// executions tracks the in-flight background executions so Shutdown can
	// wait for them.
	lifecycleMu sync.Mutex
	daemonCtx   context.Context
	daemonStop  context.CancelFunc
	stopped     bool
	handoffs    sync.WaitGroup
	executions  sync.WaitGroup
}

// deployEngine is the subset of the deployment engine the app service needs.
type deployEngine interface {
	StartDeploy(ctx context.Context, input deployment.DeployInput) (*deployment.StartDeployResult, error)
	AbandonDeploy(ctx context.Context, claim deployment.DeployClaim) error
	ExecuteDeploy(ctx context.Context, claim deployment.DeployClaim) (*deployment.DeployResult, error)
	Stop(ctx context.Context, app, opID string) (*deployment.LifecycleResult, error)
	Start(ctx context.Context, app, opID string) (*deployment.LifecycleResult, error)
	Restart(ctx context.Context, app, service, opID string) (*deployment.LifecycleResult, error)
	Remove(ctx context.Context, app, opID string) (*deployment.LifecycleResult, error)
}

// NewAppServiceImpl wires the driving-port implementation.
func NewAppServiceImpl(store out.AppState, deploy deployEngine, secrets out.SecretWriter, log zerowrap.Logger) *AppServiceImpl {
	daemonCtx, daemonStop := context.WithCancel(context.Background())
	return &AppServiceImpl{
		store:      store,
		deploy:     deploy,
		secrets:    secrets,
		log:        log,
		core:       NewService(store, log),
		daemonCtx:  daemonCtx,
		daemonStop: daemonStop,
	}
}

// WithDaemonContext sets the parent context that owns background deploy
// executions. It must be called during wiring, before the first Deploy. The
// derived context is cancelled by Shutdown; request contexts are never used
// as its parent.
func (s *AppServiceImpl) WithDaemonContext(parent context.Context) *AppServiceImpl {
	if parent == nil {
		parent = context.Background()
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopped {
		return s
	}
	ctx, cancel := context.WithCancel(parent)
	if s.daemonStop != nil {
		s.daemonStop()
	}
	s.daemonCtx, s.daemonStop = ctx, cancel
	return s
}

// Shutdown stops scheduling background deploy executions, cancels the daemon
// context so in-flight executions abort with a recoverable non-terminal
// journal, and waits for the in-flight hand-offs and executions to unwind or
// for ctx to expire. Waiting for the hand-offs first means a Deploy that
// claimed an operation just before Shutdown always schedules or settles it
// before Shutdown returns.
func (s *AppServiceImpl) Shutdown(ctx context.Context) error {
	s.lifecycleMu.Lock()
	if !s.stopped {
		s.stopped = true
		s.daemonStop()
	}
	s.lifecycleMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.handoffs.Wait()
		s.executions.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// scheduleExecution runs the claimed operation's effects on the daemon
// context. Only the owner of a freshly claimed key reaches it, so a duplicate
// key can never spawn a second execution. The journal claimed by StartDeploy
// is already durable when the goroutine starts. When Shutdown won the race
// between the claim and scheduling, the claim is settled without starting:
// the live marker is released and the journal converged as interrupted, so it
// can neither execute nor stay marked live forever.
func (s *AppServiceImpl) scheduleExecution(claim deployment.DeployClaim) error {
	opFields := map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Deploy",
		"app":                 claim.App,
		"op":                  claim.Op,
	}
	s.lifecycleMu.Lock()
	if s.stopped {
		settleCtx := zerowrap.CtxWithFields(context.WithoutCancel(s.daemonCtx), opFields)
		s.lifecycleMu.Unlock()
		settleLog := zerowrap.FromCtx(settleCtx)
		settleLog.Warn().Msg("apps: deploy claimed during shutdown; settling the claim without executing")
		if err := s.deploy.AbandonDeploy(settleCtx, claim); err != nil {
			settleLog.Warn().Err(err).Msg("apps: failed to settle a deploy claimed during shutdown")
		}
		return fmt.Errorf("apps: deploy claimed during shutdown: %w", domain.ErrAppStateConflict)
	}
	ctx := zerowrap.CtxWithFields(s.daemonCtx, opFields)
	s.executions.Add(1)
	s.lifecycleMu.Unlock()

	go func() {
		defer s.executions.Done()
		execLog := zerowrap.FromCtx(ctx)
		if _, err := s.deploy.ExecuteDeploy(ctx, claim); err != nil {
			execLog.Warn().Err(err).Msg("apps: background deploy execution failed")
		}
	}()
	return nil
}

// WithEntrypoints supplies the installation entrypoint listeners used to
// validate L4 publish declarations at apply time.
func (s *AppServiceImpl) WithEntrypoints(listeners map[string]domain.EntryPointListener) *AppServiceImpl {
	s.core.WithEntrypoints(listeners)
	return s
}

// WithGCBarrier supplies the process-wide GC barrier used to serialize an
// apply against destructive prune.
func (s *AppServiceImpl) WithGCBarrier(barrier out.GCBarrier) *AppServiceImpl {
	s.core.WithGCBarrier(barrier)
	return s
}

// WithImagePolicy supplies the registry policy used for manifest validation.
func (s *AppServiceImpl) WithImagePolicy(policy domain.ImageSourcePolicy) *AppServiceImpl {
	s.core.WithImagePolicy(policy)
	return s
}

// WithBindPolicies supplies the administrative bind policies used at apply time.
func (s *AppServiceImpl) WithBindPolicies(policies map[string]domain.AppBindPolicy) *AppServiceImpl {
	s.core.SetBindPolicies(policies)
	return s
}

// SetBindPolicies atomically replaces the administrative bind policies. It is
// safe to call from a config reload while applies are in flight.
func (s *AppServiceImpl) SetBindPolicies(policies map[string]domain.AppBindPolicy) {
	s.core.SetBindPolicies(policies)
}

// WithDevicePolicies supplies the administrative device policies used at apply time.
func (s *AppServiceImpl) WithDevicePolicies(policies map[string]domain.AppDevicePolicy) *AppServiceImpl {
	s.core.SetDevicePolicies(policies)
	return s
}

// SetDevicePolicies atomically replaces the administrative device policies. It is
// safe to call from a config reload while applies are in flight.
func (s *AppServiceImpl) SetDevicePolicies(policies map[string]domain.AppDevicePolicy) {
	s.core.SetDevicePolicies(policies)
}

var _ in.AppService = (*AppServiceImpl)(nil)

// Apply implements in.AppService.
func (s *AppServiceImpl) Apply(ctx context.Context, spec domain.AppSpec, source []byte, dryRun bool) (*in.AppApplyResult, *in.AppDryRunResult, error) {
	applyResult, dryResult, err := s.core.Apply(ctx, spec, source, dryRun)
	if err != nil {
		return nil, nil, err
	}
	if dryResult != nil {
		return nil, &in.AppDryRunResult{
			App:   spec.Name,
			Valid: true,
			Diff:  dryResult.Diff,
		}, nil
	}
	if applyResult == nil {
		return nil, &in.AppDryRunResult{App: spec.Name, Valid: true}, nil
	}
	return &in.AppApplyResult{
		App:               applyResult.App,
		FormerRevision:    applyResult.FormerRevision,
		ResultingRevision: applyResult.ResultingRevision,
		Noop:              applyResult.Noop,
		Pending:           applyResult.Pending,
		Diff:              applyResult.Diff,
		IntentID:          applyResult.IntentID,
	}, nil, nil
}

// List implements in.AppService. Names whose only remaining record is a
// retired operation journal are not apps any more and are not listed.
func (s *AppServiceImpl) List(ctx context.Context) ([]in.AppSummary, error) {
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(apps)
	summaries := make([]in.AppSummary, 0, len(apps))
	for _, app := range apps {
		live, err := s.store.AppExists(ctx, app)
		if err != nil {
			return nil, err
		}
		if !live {
			continue
		}
		summary, err := s.summary(ctx, app)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// summary builds one list row from desired + ACTIVE + intent + the latest
// journal outcome.
func (s *AppServiceImpl) summary(ctx context.Context, app string) (in.AppSummary, error) {
	desired, _, err := s.store.LoadDesired(ctx, app)
	if err != nil {
		return in.AppSummary{}, err
	}
	active, ok, err := s.store.LoadActive(ctx, app)
	if err != nil {
		return in.AppSummary{}, err
	}
	intent, err := s.store.LoadIntent(ctx, app)
	if err != nil {
		return in.AppSummary{}, err
	}
	summary := in.AppSummary{
		App:           app,
		Desired:       desired.Revision,
		DesiredStatus: desired.Status,
		Pending:       desiredPending(desired.Revision, active, ok),
		Stopped:       intent.Stopped,
	}
	if ok {
		summary.Active = active.ConvergedRevision
		summary.Converged = active.Converged
	}
	latest, hasOp, err := s.store.LoadLatestOperation(ctx, app)
	if err != nil {
		return in.AppSummary{}, err
	}
	if hasOp {
		summary.LastOutcome = latest.Outcome
	}
	return summary, nil
}

// Show implements in.AppService.
func (s *AppServiceImpl) Show(ctx context.Context, app string) (*in.AppDetail, error) {
	live, err := s.store.AppExists(ctx, app)
	if err != nil {
		return nil, err
	}
	if !live {
		return nil, fmt.Errorf("apps: app %q does not exist: %w", app, domain.ErrAppNotFound)
	}
	desired, _, err := s.store.LoadDesired(ctx, app)
	if err != nil {
		return nil, err
	}
	active, ok, err := s.store.LoadActive(ctx, app)
	if err != nil {
		return nil, err
	}
	intent, err := s.store.LoadIntent(ctx, app)
	if err != nil {
		return nil, err
	}
	detail := &in.AppDetail{
		App:               app,
		DesiredRevision:   desired.Revision,
		DesiredStatus:     desired.Status,
		Pending:           desiredPending(desired.Revision, active, ok),
		Converged:         active.Converged,
		ConvergedRevision: active.ConvergedRevision,
		Services:          map[string]in.AppServiceView{},
		Stopped:           intent.Stopped,
	}
	ownership, err := s.store.LoadOwnership(ctx, app)
	if err != nil {
		return nil, err
	}
	detail.Retained = retainedView(ownership)
	for name, svc := range active.Services {
		detail.Services[name] = in.AppServiceView{
			EffectiveRevision: svc.EffectiveRevision,
			Digest:            svc.Digest,
			Container:         svc.Container,
			RestartUnsafe:     ownership.Services[name].RestartUnsafe,
		}
	}
	latest, ok, err := s.store.LoadLatestOperation(ctx, app)
	if err != nil {
		return nil, err
	}
	if ok {
		detail.LastOp = latest.Op
		detail.LastOpKind = latest.Kind
		detail.LastOutcome = latest.Outcome
		detail.LastOpStartedAt = latest.StartedAt
	}
	return detail, nil
}

// desiredPending reports desired state the active revision has not
// reached. It reads desired vs ACTIVE only: a journal outcome never
// decides convergence.
func desiredPending(desiredRevision string, active domain.AppActive, hasActive bool) bool {
	if desiredRevision == "" {
		return false
	}
	return !hasActive || !active.Converged || active.ConvergedRevision != desiredRevision
}

// retainedView lists the resources the app owns and would retain on
// removal, sorted for stable output.
func retainedView(ownership domain.AppOwnership) in.AppRetainedView {
	view := in.AppRetainedView{}
	for _, volume := range ownership.Volumes {
		name := volume.RuntimeName
		if name == "" {
			name = volume.Name
		}
		view.Volumes = append(view.Volumes, name)
	}
	for _, secret := range ownership.Secrets {
		view.Secrets = append(view.Secrets, secret.Path)
	}
	for _, image := range ownership.Images {
		view.Images = append(view.Images, image.Reference)
	}
	sort.Strings(view.Volumes)
	sort.Strings(view.Secrets)
	sort.Strings(view.Images)
	return view
}

// Diff implements in.AppService.
func (s *AppServiceImpl) Diff(ctx context.Context, app string) (domain.AppDiff, error) {
	desired, ok, err := s.store.LoadDesired(ctx, app)
	if err != nil {
		return domain.AppDiff{}, err
	}
	if !ok {
		return domain.AppDiff{}, fmt.Errorf("apps: app %q has no desired state: %w", app, domain.ErrAppRevisionNotFound)
	}
	active, _, err := s.store.LoadActive(ctx, app)
	if err != nil {
		return domain.AppDiff{}, err
	}
	return domain.DiffAppSpec(desired.Spec, activeSpec(active)), nil
}

// Deploy implements in.AppService. It enrolls the request hand-off with the
// lifecycle first, so a deploy arriving after shutdown began is refused before
// any claim, and one already in flight is always scheduled or settled before
// Shutdown returns. StartDeploy then claims and persists the operation under
// the app lock, and the effects run in the background on the daemon context,
// so Deploy returns the still-running journal promptly and request
// cancellation cannot abort an in-flight replacement. A repeated idempotency
// key is never owned twice: its stored journal replays instead of spawning a
// second execution, and only the owner schedules any work.
func (s *AppServiceImpl) Deploy(ctx context.Context, app, revision, service, idempotencyKey string) (*domain.AppOperation, error) {
	done, err := s.beginDeploy()
	if err != nil {
		return nil, err
	}
	defer done()

	started, err := s.deploy.StartDeploy(ctx, deployment.DeployInput{App: app, Revision: revision, Service: service, Op: idempotencyKey})
	if err != nil {
		return nil, err
	}
	if !started.Owned {
		// The key already answered this request: its stored journal is the
		// result and no workload is touched again.
		return s.operationResult(ctx, app, started.Claim.Op, started.ReplayError())
	}
	if err := s.scheduleExecution(started.Claim); err != nil {
		return s.operationResult(ctx, app, started.Claim.Op, err)
	}
	return s.operationResult(ctx, app, started.Claim.Op, nil)
}

// beginDeploy enrolls one Deploy hand-off before the engine claims anything.
// It refuses the request once Shutdown began, and otherwise guarantees Shutdown
// waits for the hand-off (claim, schedule, or settle) to finish before state is
// torn down, so no claim is made or settled against closed state. The returned
// done func must be called exactly once.
func (s *AppServiceImpl) beginDeploy() (func(), error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopped {
		return nil, fmt.Errorf("apps: deploy refused: daemon is shutting down: %w", domain.ErrAppStateConflict)
	}
	s.handoffs.Add(1)
	return s.handoffs.Done, nil
}

// Stop implements in.AppService.
func (s *AppServiceImpl) Stop(ctx context.Context, app, idempotencyKey string) (*domain.AppOperation, error) {
	result, err := s.deploy.Stop(ctx, app, idempotencyKey)
	if result == nil {
		return nil, err
	}
	return s.operationResult(ctx, app, result.Op, err)
}

// Start implements in.AppService.
func (s *AppServiceImpl) Start(ctx context.Context, app, idempotencyKey string) (*domain.AppOperation, error) {
	result, err := s.deploy.Start(ctx, app, idempotencyKey)
	if result == nil {
		return nil, err
	}
	return s.operationResult(ctx, app, result.Op, err)
}

// Restart implements in.AppService.
func (s *AppServiceImpl) Restart(ctx context.Context, app, service, idempotencyKey string) (*domain.AppOperation, error) {
	result, err := s.deploy.Restart(ctx, app, service, idempotencyKey)
	if result == nil {
		return nil, err
	}
	return s.operationResult(ctx, app, result.Op, err)
}

// Remove implements in.AppService.
func (s *AppServiceImpl) Remove(ctx context.Context, app, idempotencyKey string) (*domain.AppOperation, error) {
	result, err := s.deploy.Remove(ctx, app, idempotencyKey)
	if result == nil {
		return nil, err
	}
	return s.operationResult(ctx, app, result.Op, err)
}

// operationResult loads the journal an engine call produced. A call that
// failed after its journal was claimed returns both, so callers can
// inspect the recorded outcome instead of losing it.
func (s *AppServiceImpl) operationResult(ctx context.Context, app, opID string, callErr error) (*domain.AppOperation, error) {
	if opID == "" {
		return nil, callErr
	}
	op, err := s.store.LoadOperation(ctx, app, opID)
	if err != nil {
		if callErr != nil {
			return nil, callErr
		}
		return nil, err
	}
	if callErr != nil {
		return &op, callErr
	}
	return &op, nil
}

// OperationByKey implements in.AppService.
func (s *AppServiceImpl) OperationByKey(ctx context.Context, app, key string) (*domain.AppOperation, error) {
	op, err := s.store.LoadOperation(ctx, app, key)
	if err != nil {
		return nil, err
	}
	return &op, nil
}

// ListSecrets implements in.AppService without reading secret values. A
// name with no live identity is not found; ownership-only apps remain live
// and report their registrations (possibly none).
func (s *AppServiceImpl) ListSecrets(ctx context.Context, app, service string) ([]in.AppSecretMetadata, error) {
	live, err := s.store.AppExists(ctx, app)
	if err != nil {
		return nil, fmt.Errorf("list app secrets: check app %q: %w", app, err)
	}
	if !live {
		return nil, fmt.Errorf("apps: app %q does not exist: %w", app, domain.ErrAppNotFound)
	}
	entries := []in.AppSecretMetadata{}
	desired, ok, err := s.store.LoadDesired(ctx, app)
	if err != nil {
		return nil, fmt.Errorf("list app secrets: load desired for %q: %w", app, err)
	}
	if ok {
		for _, svc := range desired.Spec.Services {
			entries = appendSecretMetadata(entries, service, svc.Name, svc.Secrets, "desired")
		}
	}
	active, ok, err := s.store.LoadActive(ctx, app)
	if err != nil {
		return nil, fmt.Errorf("list app secrets: load active for %q: %w", app, err)
	}
	if ok {
		for svcName, svc := range active.Services {
			entries = appendSecretMetadata(entries, service, svcName, svc.Spec.Secrets, "active")
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Name < b.Name
	})
	return entries, nil
}

func appendSecretMetadata(entries []in.AppSecretMetadata, filter, service string, secrets map[string]string, source string) []in.AppSecretMetadata {
	if filter != "" && filter != service {
		return entries
	}
	for key, name := range secrets {
		entries = append(entries, in.AppSecretMetadata{Service: service, Key: key, Name: name, Source: source, Presence: "unknown"})
	}
	return entries
}

// SetSecrets implements in.AppService.
func (s *AppServiceImpl) SetSecrets(ctx context.Context, app, service string, values map[string]string) error {
	if service == "" {
		return fmt.Errorf("apps: service is required: %w", domain.ErrInvalidAppSpec)
	}
	registered, err := s.secretNames(ctx, app, service)
	if err != nil {
		return err
	}
	for key, value := range values {
		name, ok := registered[key]
		if !ok {
			return fmt.Errorf("apps: secret %q not registered for %s/%s: %w", key, app, service, domain.ErrAppSecretMissing)
		}
		path, err := s.secretPath(ctx, app, service, name)
		if err != nil {
			return err
		}
		if err := s.secrets.SetSecret(ctx, path, value); err != nil {
			return fmt.Errorf("apps: write secret %q: %w", key, err)
		}
	}
	return nil
}

// DeleteSecret implements in.AppService.
func (s *AppServiceImpl) DeleteSecret(ctx context.Context, app, service, key string) error {
	if service == "" || key == "" {
		return fmt.Errorf("apps: service and key are required: %w", domain.ErrInvalidAppSpec)
	}
	registered, err := s.secretNames(ctx, app, service)
	if err != nil {
		return err
	}
	name, ok := registered[key]
	if !ok {
		return fmt.Errorf("apps: secret %q not registered for %s/%s: %w", key, app, service, domain.ErrAppSecretMissing)
	}
	// Refused while the name is still referenced by desired or active.
	if referenced, err := s.secretReferenced(ctx, app, service, name); err != nil {
		return err
	} else if referenced {
		return fmt.Errorf("apps: secret %q still referenced: %w", key, domain.ErrAppStateConflict)
	}
	path, err := s.secretPath(ctx, app, service, name)
	if err != nil {
		return err
	}
	return s.secrets.DeleteSecret(ctx, path)
}

// secretPath returns the UUID-keyed pass path for one secret name.
// Empty ID means legacy name-keyed paths (records predating UUIDs).
func (s *AppServiceImpl) secretPath(ctx context.Context, app, service, name string) (string, error) {
	ownership, err := s.store.LoadOwnership(ctx, app)
	if err != nil {
		return "", err
	}
	return domain.AppSecretPathForID(ownership.ID, app, service, name), nil
}

// secretNames returns env-key → secret-name registrations.
func (s *AppServiceImpl) secretNames(ctx context.Context, app, service string) (map[string]string, error) {
	merged := map[string]string{}
	desired, ok, err := s.store.LoadDesired(ctx, app)
	if err != nil {
		return nil, err
	}
	if ok {
		for _, svc := range desired.Spec.Services {
			if svc.Name == service {
				for envKey, name := range svc.Secrets {
					merged[envKey] = name
				}
			}
		}
	}
	active, ok, err := s.store.LoadActive(ctx, app)
	if err != nil {
		return nil, err
	}
	if ok {
		for name, svc := range active.Services {
			if name == service {
				for envKey, secretName := range svc.Spec.Secrets {
					merged[envKey] = secretName
				}
			}
		}
	}
	if len(merged) == 0 {
		return nil, fmt.Errorf("apps: service %q has no registered secrets: %w", service, domain.ErrAppSecretMissing)
	}
	return merged, nil
}

// secretReferenced reports desired/active references to a secret name.
func (s *AppServiceImpl) secretReferenced(ctx context.Context, app, service, name string) (bool, error) {
	desired, ok, err := s.store.LoadDesired(ctx, app)
	if err != nil {
		return false, err
	}
	if ok {
		for _, svc := range desired.Spec.Services {
			if svc.Name != service {
				continue
			}
			for _, secretName := range svc.Secrets {
				if secretName == name {
					return true, nil
				}
			}
		}
	}
	active, ok, err := s.store.LoadActive(ctx, app)
	if err != nil {
		return false, err
	}
	if ok {
		for svcName, svc := range active.Services {
			if svcName != service {
				continue
			}
			for _, secretName := range svc.Spec.Secrets {
				if secretName == name {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
