package deployment

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

// Recovery pass bounds and policy constants.
const (
	// reconcileServiceDeadline bounds one service's recovery: inspect,
	// start/restart, bind inspection, readiness, and publication. A hung
	// service times out, releases its app lock, and never starves the
	// apps or the services that follow it.
	reconcileServiceDeadline = 2 * time.Minute
	// stableReadyWindow is how long a generation must be observed
	// running, ready, and published before its failure history resets.
	stableReadyWindow = 5 * time.Minute
	// unhealthyThreshold is the number of consecutive unhealthy
	// observations required before recovery restarts a workload.
	unhealthyThreshold = 2
)

// backoffDelays are the delays applied after the third consecutive
// failure: 1, 2, 4, 8, then a 15-minute cap.
var backoffDelays = []time.Duration{
	1 * time.Minute,
	2 * time.Minute,
	4 * time.Minute,
	8 * time.Minute,
	15 * time.Minute,
}

// recoveryKey identifies one workload generation for recovery state.
type recoveryKey struct {
	app       string
	service   string
	container string
}

// recoveryBackoff is the in-memory crash-loop budget, keyed by
// (app, service, exact container ID). It is intentionally not durable:
// a daemon restart re-arms one real attempt per generation.
type recoveryBackoff struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[recoveryKey]*backoffEntry
}

type backoffEntry struct {
	failures     int
	nextAttempt  time.Time
	healthySince time.Time
	unhealthy    int
	// attemptPending marks a recovery intervention (start/restart) that
	// has not yet been confirmed by a stable, verified observation. A
	// generation found down again while an attempt is still pending is
	// a crash loop and is charged, so a workload cannot restart every
	// pass forever just because each individual start call succeeded.
	attemptPending bool
}

func newRecoveryBackoff(now func() time.Time) *recoveryBackoff {
	return &recoveryBackoff{now: now, entries: map[recoveryKey]*backoffEntry{}}
}

// allow reports whether one real attempt may run now.
func (b *recoveryBackoff) allow(key recoveryKey) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.entries[key]
	if !ok {
		return true
	}
	return !b.now().Before(entry.nextAttempt)
}

// recordFailure charges one failed start/restart or unhealthy restart.
// The first three consecutive failures are real attempts; the fourth and
// later ones delay the next attempt by 1, 2, 4, 8, then 15 minutes.
// Only five continuously stable minutes (observeStable) or an explicit
// forget reset the escalation, so a crash-looping generation cannot
// escape its budget by waiting.
func (b *recoveryBackoff) recordFailure(key recoveryKey) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	entry := b.ensure(key)
	entry.failures++
	entry.healthySince = time.Time{}
	if entry.failures >= 3 {
		index := entry.failures - 3
		if index >= len(backoffDelays) {
			index = len(backoffDelays) - 1
		}
		entry.nextAttempt = now.Add(backoffDelays[index])
	}
}

// observeUnhealthy records one consecutive unhealthy observation and
// returns the current streak length. `starting` and transient health
// errors must not reach this method.
func (b *recoveryBackoff) observeUnhealthy(key recoveryKey) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.ensure(key)
	entry.unhealthy++
	return entry.unhealthy
}

// clearUnhealthy drops the unhealthy streak after a non-unhealthy
// observation.
func (b *recoveryBackoff) clearUnhealthy(key recoveryKey) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry, ok := b.entries[key]; ok {
		entry.unhealthy = 0
	}
}

// observeStable records a running, ready, published observation and
// resets the generation's history after stableReadyWindow. It never
// creates an entry: a generation with no recorded history is stable by
// definition, so healthy services do not accumulate monitor state.
func (b *recoveryBackoff) observeStable(key recoveryKey) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.entries[key]
	if !ok {
		return
	}
	now := b.now()
	entry.unhealthy = 0
	if entry.healthySince.IsZero() {
		entry.healthySince = now
		return
	}
	if now.Sub(entry.healthySince) >= stableReadyWindow {
		// Five continuously stable minutes: the generation is considered
		// recovered, so its failure history and unconfirmed-attempt flag
		// are dropped. An intervention is therefore never confirmed by a
		// single good pass.
		delete(b.entries, key)
	}
}

// invalidateStable drops the stability streak without charging a
// failure: an unhealthy, transitional, or unverifiable observation is
// not five continuously stable minutes.
func (b *recoveryBackoff) invalidateStable(key recoveryKey) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry, ok := b.entries[key]; ok {
		entry.healthySince = time.Time{}
	}
}

// markAttempt records a recovery intervention that must be confirmed by
// a later stable observation.
func (b *recoveryBackoff) markAttempt(key recoveryKey) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensure(key).attemptPending = true
}

// attemptPending reports whether a previous recovery intervention of
// this generation was never confirmed stable.
func (b *recoveryBackoff) attemptPending(key recoveryKey) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.entries[key]
	return ok && entry.attemptPending
}

// ensure returns the entry for one key, creating it on first use. The
// caller must hold mu.
func (b *recoveryBackoff) ensure(key recoveryKey) *backoffEntry {
	entry, ok := b.entries[key]
	if !ok {
		entry = &backoffEntry{}
		b.entries[key] = entry
	}
	return entry
}

// forget drops one generation's history (stopped, removed, or replaced).
func (b *recoveryBackoff) forget(key recoveryKey) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.entries, key)
}

// publicationInhibition is the in-memory record of services whose
// fail-closed traffic withdrawal could not be applied. Such a service
// must withdraw successfully again before any later publication.
type publicationInhibition struct {
	mu      sync.Mutex
	entries map[recoveryKey]struct{}
}

func (p *publicationInhibition) mark(app, service string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries == nil {
		p.entries = map[recoveryKey]struct{}{}
	}
	p.entries[recoveryKey{app: app, service: service}] = struct{}{}
}

func (p *publicationInhibition) clear(app, service string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.entries, recoveryKey{app: app, service: service})
}

func (p *publicationInhibition) inhibited(app, service string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.entries[recoveryKey{app: app, service: service}]
	return ok
}

// pending returns the services of one app whose withdrawal is still
// unproven, so a caller can retry them before publishing anything.
func (p *publicationInhibition) pending(app string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var services []string
	for key := range p.entries {
		if key.app == app {
			services = append(services, key.service)
		}
	}
	sort.Strings(services)
	return services
}

// retainAppServices drops entries only for services no longer present in
// ACTIVE. Marks for active services survive failed passes until a later
// withdrawal is proven, so another publisher cannot restore stale traffic.
func (p *publicationInhibition) retainAppServices(app string, live map[string]struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key := range p.entries {
		if key.app != app {
			continue
		}
		if _, ok := live[key.service]; !ok {
			delete(p.entries, key)
		}
	}
}

// executionTracker remembers the last observed execution start of each
// generation so a native runtime restart is detected without Gordon
// restarting the workload itself.
type executionTracker struct {
	mu   sync.Mutex
	seen map[recoveryKey]time.Time
}

// isNewExecution reports whether this observation still needs verification.
// An unseen generation is treated as fresh: the daemon may have been absent
// while the runtime restarted it, so accepting it without readiness and bind
// verification could republish stale traffic. The observation is recorded
// only after verification succeeds.
func (t *executionTracker) isNewExecution(key recoveryKey, startedAt time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	previous, ok := t.seen[key]
	return !ok || !previous.Equal(startedAt)
}

// record stores the verified execution of one generation.
func (t *executionTracker) record(key recoveryKey, startedAt time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = map[recoveryKey]time.Time{}
	}
	t.seen[key] = startedAt
}

// forget drops one generation's observation.
func (t *executionTracker) forget(key recoveryKey) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.seen, key)
}

// ReconcileRunning is the daemon-owned periodic recovery pass. It never
// consults DESIRED to select content, never changes intent, never
// pulls, creates, or removes a container, and never starts a
// container whose ID is absent from ACTIVE. Every app is attempted;
// failures are aggregated so one failing app cannot hide the others.
func (s *Service) ReconcileRunning(ctx context.Context) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "ReconcileRunning",
	})
	if s.deps.State == nil {
		return nil
	}
	apps, err := s.deps.State.ListApps(ctx)
	if err != nil {
		return fmt.Errorf("deployment: list apps for reconciliation: %w", err)
	}
	var failures []error
	for _, app := range apps {
		// Bound the whole app, not each service cumulatively. A multi-service
		// app with hung runtime calls must not starve every later app forever.
		appCtx, cancel := context.WithTimeout(ctx, reconcileServiceDeadline)
		err := s.reconcileAppRunning(appCtx, app)
		cancel()
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// reconcileAppRunning recovers one app. A busy app (an in-flight user
// mutation) is skipped, never waited on: the periodic pass must not
// starve later apps or shutdown.
func (s *Service) reconcileAppRunning(ctx context.Context, app string) error {
	release, ok := s.tryAcquireAppContext(ctx, app)
	if !ok {
		s.log.Debug().Str("app", app).Msg("deployment: reconciliation skipped, app is busy")
		return nil
	}
	defer release()

	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "ReconcileRunning",
		"app":                 app,
	})
	intent, err := s.deps.State.LoadIntent(ctx, app)
	if err != nil {
		return fmt.Errorf("deployment: reconcile %q: load intent: %w", app, err)
	}
	active, ok, err := s.deps.State.LoadActive(ctx, app)
	if err != nil {
		return fmt.Errorf("deployment: reconcile %q: load active: %w", app, err)
	}
	if !ok || len(active.Services) == 0 {
		s.pruneRecoveryHistory(app, active)
		return nil
	}
	inhibitions, err := s.deps.State.LoadRecoveryInhibitions(ctx, app)
	if err != nil {
		return fmt.Errorf("deployment: reconcile %q: load recovery inhibitions: %w", app, err)
	}
	inhibited := map[recoveryKey]struct{}{}
	for _, inhibition := range inhibitions {
		inhibited[recoveryKey{app: app, service: inhibition.Service, container: inhibition.ContainerID}] = struct{}{}
	}
	var failures []error
	for _, name := range sortedServiceNames(active) {
		eff := active.Services[name]
		if intent.Stopped {
			if err := s.convergeStoppedService(ctx, app, name, eff); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		if err := s.reconcileRunningService(ctx, app, name, eff, inhibited); err != nil {
			failures = append(failures, err)
		}
	}
	if intent.Stopped {
		// A stopped app must not stay published: rebuild the projection
		// so no revoked backend remains routable. The publication is
		// bounded like any other recovery effect.
		publishCtx, cancel := context.WithTimeout(ctx, reconcileServiceDeadline)
		if err := s.refreshTraffic(publishCtx, app); err != nil {
			failures = append(failures, err)
		}
		cancel()
	}
	s.pruneRecoveryHistory(app, active)
	return errors.Join(failures...)
}

// convergeStoppedService enforces durable stopped intent: it never
// starts or publishes, and it stops a verified running exact ACTIVE ID
// without deleting the container or its volumes.
func (s *Service) convergeStoppedService(ctx context.Context, app, name string, eff domain.AppEffectiveService) error {
	key := recoveryKey{app: app, service: name, container: eff.Container}
	s.backoff.forget(key)
	s.executions.forget(key)
	if eff.Container == "" || s.deps.Runtime == nil {
		return nil
	}
	serviceCtx, cancel := context.WithTimeout(ctx, reconcileServiceDeadline)
	defer cancel()
	container, err := s.deps.Runtime.InspectContainer(serviceCtx, eff.Container)
	if err != nil {
		if errors.Is(err, domain.ErrContainerNotFound) {
			return nil
		}
		return fmt.Errorf("deployment: stopped app %q service %q: inspect %s: %w", app, name, eff.Container, err)
	}
	if container.Status != string(domain.ContainerStatusRunning) && !isTransitionalStatus(container.Status) {
		return nil
	}
	if err := s.deps.Runtime.StopContainer(serviceCtx, eff.Container, serviceStopGrace(eff)); err != nil && !errors.Is(err, domain.ErrContainerNotFound) {
		return fmt.Errorf("deployment: stopped app %q service %q: stop %s: %w", app, name, eff.Container, err)
	}
	s.log.Info().Str("app", app).Str("service", name).Str("container", eff.Container).
		Msg("deployment: stopped intent enforced on a revived container")
	return nil
}

// reconcileRunningService recovers one service to the running intent
// carried by ACTIVE. It only ever acts on the exact ACTIVE container ID.
func (s *Service) reconcileRunningService(ctx context.Context, app, name string, eff domain.AppEffectiveService, inhibited map[recoveryKey]struct{}) error {
	if eff.Container == "" {
		// Absent ACTIVE: never act, never reconstruct.
		return nil
	}
	if _, blocked := inhibited[recoveryKey{app: app, service: name, container: eff.Container}]; blocked {
		return s.refuseInhibitedRecovery(ctx, app, name, eff.Container)
	}
	if s.deps.Runtime == nil {
		return nil
	}
	serviceCtx, cancel := context.WithTimeout(ctx, reconcileServiceDeadline)
	defer cancel()
	key := recoveryKey{app: app, service: name, container: eff.Container}
	container, fresh, err := s.inspectForRecovery(serviceCtx, app, name, eff, key)
	if err != nil {
		return err
	}
	return s.convergeRecoveredService(serviceCtx, app, name, eff, container, key, fresh)
}

// inspectForRecovery retries a pending withdrawal, classifies the exact
// ACTIVE container, and makes sure nothing unverifiable stays routable
// before the workload is touched. A transient inspection error never
// changes the workload or its traffic.
func (s *Service) inspectForRecovery(ctx context.Context, app, name string, eff domain.AppEffectiveService, key recoveryKey) (*domain.Container, bool, error) {
	// A previous withdrawal that could not be applied must be retried
	// before anything else: never publish on top of stale forwarding.
	withdrawn := false
	if s.publication.inhibited(app, name) {
		if err := s.withdrawForRecovery(ctx, app, name); err != nil {
			return nil, false, err
		}
		withdrawn = true
	}
	container, err := s.deps.Runtime.InspectContainer(ctx, eff.Container)
	if err != nil {
		return nil, false, s.recoveryInspectError(ctx, app, name, eff, err)
	}
	// Never serve a backend that is not verifiably serving: a native
	// restart (fresh execution) or a container that is not running must
	// be withdrawn, and that withdrawal must succeed, before Gordon
	// touches the workload. A transitional state keeps its traffic: the
	// runtime is restarting it right now and it will be back within the
	// pass cadence.
	fresh := s.executions.isNewExecution(key, container.StartedAt)
	unverified := container.Status != string(domain.ContainerStatusRunning) && !isTransitionalStatus(container.Status)
	if (fresh || unverified) && !withdrawn {
		if err := s.withdrawForRecovery(ctx, app, name); err != nil {
			return nil, false, err
		}
	}
	if fresh {
		s.backoff.clearUnhealthy(key)
	}
	return container, fresh, nil
}

// convergeRecoveredService drives the exact ACTIVE container to a
// verified, published state, or leaves it withdrawn with a reported
// reason.
func (s *Service) convergeRecoveredService(ctx context.Context, app, name string, eff domain.AppEffectiveService, container *domain.Container, key recoveryKey, fresh bool) error {
	started, verify, err := s.driveRecoveredContainer(ctx, app, name, eff, container, key)
	if err != nil {
		return errors.Join(s.withdrawForRecovery(ctx, app, name), err)
	}
	if !verify {
		// Transitional or backoff-delayed: observe now, converge later.
		s.backoff.invalidateStable(key)
		return nil
	}
	restarted, stable, err := s.recoverHealth(ctx, app, name, eff, key)
	if err != nil {
		s.backoff.invalidateStable(key)
		return errors.Join(s.withdrawForRecovery(ctx, app, name), err)
	}
	if err := s.publishRecoveredService(ctx, app, name, eff, container, key, started, restarted, fresh); err != nil {
		return err
	}
	if stable {
		s.backoff.observeStable(key)
	} else {
		s.backoff.invalidateStable(key)
	}
	return nil
}

// refuseInhibitedRecovery reports a generation whose recovery is durably
// inhibited and withdraws its backend best-effort: it must not stay
// routable, and it must never be started.
func (s *Service) refuseInhibitedRecovery(ctx context.Context, app, name, containerID string) error {
	withdrawCtx, cancel := context.WithTimeout(ctx, reconcileServiceDeadline)
	defer cancel()
	return errors.Join(
		fmt.Errorf(
			"deployment: app %q service %q container %s has recovery inhibited; no start attempted",
			app, name, containerID,
		),
		s.withdrawForRecovery(withdrawCtx, app, name),
	)
}

// recoveryInspectError classifies one failed inspection: a confirmed
// missing container withdraws traffic and reports without reconstruction,
// while a transient error leaves the workload and its traffic untouched.
func (s *Service) recoveryInspectError(ctx context.Context, app, name string, eff domain.AppEffectiveService, err error) error {
	if !errors.Is(err, domain.ErrContainerNotFound) {
		return fmt.Errorf("deployment: app %q service %q inspect %s: %w", app, name, eff.Container, err)
	}
	// The container is confirmed gone: its loopback reservations must not
	// outlive it, or a later workload that reuses the port would collide
	// with a claim no container holds.
	releaseErr := s.deps.State.ReleaseBackendBinds(ctx, app, eff.Container)
	if releaseErr != nil {
		releaseErr = fmt.Errorf("deployment: release claims of gone container %s: %w", eff.Container, releaseErr)
	}
	return errors.Join(
		s.withdrawForRecovery(ctx, app, name),
		releaseErr,
		fmt.Errorf("deployment: app %q service %q container %s is gone; traffic withdrawn, no reconstruction: %w",
			app, name, eff.Container, domain.ErrContainerNotFound),
	)
}

// driveRecoveredContainer brings the exact ACTIVE container to a running
// state: an already running container is left alone, a transitional one
// is observed and retried later, an unsafe state errors non-destructively,
// and a stopped one is started under the generation's backoff budget.
// verify reports whether the caller may continue to binds, readiness, and
// publication: a transitional or delayed generation must not be published
// on this pass.
func (s *Service) driveRecoveredContainer(ctx context.Context, app, name string, eff domain.AppEffectiveService, container *domain.Container, key recoveryKey) (started bool, verify bool, err error) {
	switch {
	case container.Status == string(domain.ContainerStatusRunning):
		return false, true, nil
	case isTransitionalStatus(container.Status):
		// restarting/starting: observe and retry on a later pass.
		return false, false, nil
	case container.Status == string(domain.ContainerStatusPaused) || container.Status == string(domain.ContainerStatusUnknown) || container.Status == "":
		return false, false, fmt.Errorf("deployment: app %q service %q container %s is %q; refusing non-destructive recovery",
			app, name, eff.Container, container.Status)
	default:
		// created/exited/dead/stopped with running intent: start the
		// exact ACTIVE ID regardless of exit code.
		if !s.backoff.allow(key) {
			return false, false, nil
		}
		// A generation found down again before a previous recovery was
		// ever confirmed stable is crash-looping: charge it, so a
		// workload that dies after every successful start cannot be
		// restarted on every pass forever.
		if s.backoff.attemptPending(key) {
			s.backoff.recordFailure(key)
			if !s.backoff.allow(key) {
				return false, false, nil
			}
		}
		started, err := s.startRecovered(ctx, app, name, eff, key)
		return started, err == nil, err
	}
}

// publishRecoveredService verifies one generation in the order the plan
// requires: read the container's live binds, probe readiness against
// them, revalidate execution identity and ACTIVE, persist the verified
// binds, and only then publish. Binds are never persisted to ACTIVE
// before readiness, so no other publication can project an unverified
// backend while this pass waits for the workload. The execution is
// recorded only on success, so a failed verification is retried on the
// next pass instead of being silently accepted.
func (s *Service) publishRecoveredService(ctx context.Context, app, name string, eff domain.AppEffectiveService, observed *domain.Container, key recoveryKey, started, restarted, fresh bool) error {
	binds, udpBinds, err := s.inspectBackendBinds(ctx, app, name, eff.Container, backendPorts(eff.Spec))
	if err != nil {
		s.backoff.recordFailure(key)
		return errors.Join(s.withdrawForRecovery(ctx, app, name), err)
	}
	if started || restarted || fresh {
		if err := waitServiceReadyWithDeps(ctx, s.probeDeps(), eff.Container, eff.Spec, binds); err != nil {
			s.backoff.recordFailure(key)
			return errors.Join(s.withdrawForRecovery(ctx, app, name), err)
		}
	}
	verifiedBinds, verifiedUDPBinds, err := s.revalidateAndPersistRecoveredBinds(ctx, app, name, eff, observed, binds, udpBinds)
	if err != nil {
		s.backoff.recordFailure(key)
		return errors.Join(s.withdrawForRecovery(ctx, app, name), err)
	}
	if started || restarted || fresh || !equalBinds(verifiedBinds, eff.BackendBinds) || !equalBinds(verifiedUDPBinds, eff.UDPBackendBinds) {
		if err := s.refreshTraffic(ctx, app); err != nil {
			return err
		}
	}
	s.executions.record(key, observed.StartedAt)
	return nil
}

// startRecovered starts the exact ACTIVE container. A failed start is
// re-inspected: if native restart policy already revived it, no failure
// is charged and no second restart is issued.
func (s *Service) startRecovered(serviceCtx context.Context, app, name string, eff domain.AppEffectiveService, key recoveryKey) (bool, error) {
	if err := s.deps.Runtime.StartContainer(serviceCtx, eff.Container); err != nil {
		if errors.Is(err, domain.ErrContainerNotFound) {
			s.backoff.forget(key)
			return false, errors.Join(
				fmt.Errorf("deployment: app %q service %q container %s is gone: %w", app, name, eff.Container, domain.ErrContainerNotFound),
			)
		}
		recovered, inspectErr := s.deps.Runtime.InspectContainer(serviceCtx, eff.Container)
		if inspectErr == nil && (recovered.Status == string(domain.ContainerStatusRunning) || isTransitionalStatus(recovered.Status)) {
			s.log.Info().Str("app", app).Str("service", name).Str("container", eff.Container).
				Msg("deployment: native restart won the race, no recovery charged")
			s.backoff.clearUnhealthy(key)
			return true, nil
		}
		s.backoff.recordFailure(key)
		return false, fmt.Errorf("deployment: app %q service %q start %s: %w", app, name, eff.Container, err)
	}
	s.backoff.markAttempt(key)
	s.log.Info().Str("app", app).Str("service", name).Str("container", eff.Container).
		Msg("deployment: recovery started the exact active container")
	return true, nil
}

// recoverHealth restarts a workload that reported `unhealthy` on
// consecutive passes. `starting` waits; a missing healthcheck never
// triggers a restart. Restarts share the generation's backoff budget and
// are marked as unconfirmed attempts. The second result reports whether
// the observation counts as stable ("running, ready, published"): an
// unhealthy, still-starting, or unreadable health state must not extend
// the five-minute stability window.
func (s *Service) recoverHealth(serviceCtx context.Context, app, name string, eff domain.AppEffectiveService, key recoveryKey) (restarted bool, stable bool, err error) {
	status, hasHealthcheck, statusErr := s.deps.Runtime.GetContainerHealthStatus(serviceCtx, eff.Container)
	if statusErr != nil || !hasHealthcheck {
		// A transient health error is not an unhealthy workload, but it
		// is also not a proven healthy one.
		return false, statusErr == nil, nil
	}
	if status != "unhealthy" {
		s.backoff.clearUnhealthy(key)
		return false, status != "starting", nil
	}
	if s.backoff.observeUnhealthy(key) < unhealthyThreshold {
		return false, false, nil
	}
	if !s.backoff.allow(key) {
		return false, false, nil
	}
	if err := s.deps.Runtime.RestartContainer(serviceCtx, eff.Container, serviceStopGrace(eff)); err != nil {
		s.backoff.recordFailure(key)
		return false, false, fmt.Errorf("deployment: app %q service %q restart unhealthy %s: %w", app, name, eff.Container, err)
	}
	s.backoff.markAttempt(key)
	s.backoff.clearUnhealthy(key)
	s.log.Info().Str("app", app).Str("service", name).Str("container", eff.Container).
		Msg("deployment: restarted an unhealthy container")
	return true, false, nil
}

// revalidateAndPersistRecoveredBinds revalidates ACTIVE and the observed
// execution immediately before publication, then persists the verified
// binds (already re-read from the exact container) into the ACTIVE
// record. Stale evidence never publishes: a native restart that happened
// while this pass waited for readiness changes the observed start and
// aborts the pass instead of publishing the previous execution's binds.
func (s *Service) revalidateAndPersistRecoveredBinds(ctx context.Context, app, name string, eff domain.AppEffectiveService, observed *domain.Container, binds, udpBinds map[int]int) (map[int]int, map[int]int, error) {
	current, err := s.deps.Runtime.InspectContainer(ctx, eff.Container)
	if err != nil {
		return nil, nil, fmt.Errorf("deployment: re-inspect %s before publication: %w", eff.Container, err)
	}
	if observed != nil && !current.StartedAt.Equal(observed.StartedAt) {
		return nil, nil, fmt.Errorf("deployment: app %q service %q container %s restarted during recovery: %w",
			app, name, eff.Container, domain.ErrAppStateConflict)
	}
	active, ok, err := s.deps.State.LoadActive(ctx, app)
	if err != nil {
		return nil, nil, fmt.Errorf("deployment: reload active for recovery: %w", err)
	}
	if !ok {
		return nil, nil, fmt.Errorf("deployment: app %q has no active state: %w", app, domain.ErrAppStateConflict)
	}
	recorded, ok := active.Services[name]
	if !ok || recorded.Container != eff.Container || recorded.EffectiveRevision != eff.EffectiveRevision {
		return nil, nil, fmt.Errorf("deployment: app %q service %q generation changed during recovery: %w", app, name, domain.ErrAppStateConflict)
	}
	if !equalBinds(binds, recorded.BackendBinds) || !equalBinds(udpBinds, recorded.UDPBackendBinds) {
		recorded.BackendBinds = binds
		recorded.UDPBackendBinds = udpBinds
		active.Services[name] = recorded
		if err := s.deps.State.SaveActive(ctx, active); err != nil {
			return nil, nil, fmt.Errorf("deployment: persist recovered binds: %w", err)
		}
	}
	return binds, udpBinds, nil
}

// withdrawForRecovery applies one serialized fail-closed withdrawal of
// a service. A failed application retains an in-memory publication
// inhibition and never releases backend claims.
func (s *Service) withdrawForRecovery(ctx context.Context, app, service string) error {
	if s.deps.Traffic == nil {
		return nil
	}
	if err := s.deps.Traffic.WithdrawService(ctx, app, service); err != nil {
		s.publication.mark(app, service)
		return fmt.Errorf("deployment: withdraw %s/%s: %w", app, service, err)
	}
	s.publication.clear(app, service)
	return nil
}

// recoveryInhibited reports whether one exact generation is durably
// inhibited for recovery.
func (s *Service) recoveryInhibited(ctx context.Context, app, service, containerID string) (bool, error) {
	if containerID == "" {
		return false, nil
	}
	inhibitions, err := s.deps.State.LoadRecoveryInhibitions(ctx, app)
	if err != nil {
		return false, fmt.Errorf("deployment: load recovery inhibitions: %w", err)
	}
	for _, inhibition := range inhibitions {
		if inhibition.Service == service && inhibition.ContainerID == containerID {
			return true, nil
		}
	}
	return false, nil
}

// inhibitRecovery durably records a generation-scoped recovery
// inhibition for one exact container ID.
func (s *Service) inhibitRecovery(ctx context.Context, app, service, containerID, reason, operation string) error {
	if containerID == "" {
		return nil
	}
	inhibition := domain.AppRecoveryInhibition{
		App:         app,
		Service:     service,
		ContainerID: containerID,
		Reason:      reason,
		Operation:   operation,
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.deps.State.SaveRecoveryInhibition(ctx, inhibition); err != nil {
		return fmt.Errorf("deployment: record recovery inhibition for %s/%s: %w", app, containerID, err)
	}
	return nil
}

// clearRecoveryInhibition drops the inhibition of a safely superseded
// generation.
func (s *Service) clearRecoveryInhibition(ctx context.Context, app, service, containerID string) error {
	if containerID == "" {
		return nil
	}
	if err := s.deps.State.ClearRecoveryInhibition(ctx, app, service, containerID); err != nil {
		return fmt.Errorf("deployment: clear recovery inhibition for %s/%s: %w", app, containerID, err)
	}
	return nil
}

// pruneRecoveryHistory drops in-memory state of generations that are no
// longer present in ACTIVE. Durable inhibition records are never
// dropped here: only an explicit supersede or removal clears them.
func (s *Service) pruneRecoveryHistory(app string, active domain.AppActive) {
	live := map[recoveryKey]struct{}{}
	liveServices := map[string]struct{}{}
	for name, svc := range active.Services {
		live[recoveryKey{app: app, service: name, container: svc.Container}] = struct{}{}
		liveServices[name] = struct{}{}
	}
	s.backoff.mu.Lock()
	for key := range s.backoff.entries {
		if key.app != app {
			continue
		}
		if _, ok := live[key]; !ok {
			delete(s.backoff.entries, key)
		}
	}
	s.backoff.mu.Unlock()
	s.executions.mu.Lock()
	for key := range s.executions.seen {
		if key.app != app {
			continue
		}
		if _, ok := live[key]; !ok {
			delete(s.executions.seen, key)
		}
	}
	s.executions.mu.Unlock()
	s.publication.retainAppServices(app, liveServices)
}

// isTransitionalStatus reports runtime states that must be observed and
// retried rather than acted on.
func isTransitionalStatus(status string) bool {
	return status == "restarting" || status == "starting"
}
