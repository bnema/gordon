// Package deployment implements the app deployment engine: preflight,
// journaled execution, and terminal-result computation.
package deployment

import (
	"context"
	"encoding/hex"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/google/uuid"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// Deps are the driven adapters owned by the deployment engine.
type Deps struct {
	State   out.AppState
	Runtime out.ContainerRuntime
	Images  out.ImageResolver
	Secrets out.SecretProvider
	// Registry configures image pulls from the installation registry.
	// Domain scopes which refs pull with auth; empty disables pulling.
	Registry RegistryConfig
	// Networks is the installation network policy applied to every app
	// workload: the prefix used to derive Gordon-owned network names and
	// whether those networks block external egress.
	Networks NetworkConfig
	// Limits are the installation container resource limits applied on
	// every create and recovery path. Zero means unlimited.
	Limits ResourceLimits
	// ImagePolicy restricts which registries and references may be
	// resolved, pulled, or run. It is enforced on every path, including
	// boot, restart, and recovery.
	ImagePolicy domain.ImageSourcePolicy
	// Traffic is the serialized HTTP/L4 publish boundary. Rebuilt after
	// every activation (deploy/start/restart/stop/remove) so the proxy
	// host index follows ACTIVE; recovery also withdraws one service
	// fail-closed before touching a workload. Nil in tests that assert
	// state effects only. Failures are reported to the caller.
	Traffic out.AppTrafficRefresher
}

// RegistryConfig carries the installation registry identity for pulls.
type RegistryConfig struct {
	Domain      string
	PullAddress string
	Username    string
	Password    string
}

// NetworkConfig carries the installation network policy for app workloads.
type NetworkConfig struct {
	// Prefix names the Gordon-owned networks. Empty uses the domain default.
	Prefix string
	// Internal blocks external egress from containers attached to those
	// networks (Docker's Internal flag).
	Internal bool
}

// ResourceLimits are the installation container limits applied on every
// create and recovery path. Zero values mean unlimited.
type ResourceLimits struct {
	MemoryBytes int64
	NanoCPUs    int64
	PidsLimit   int64
}

// Service orchestrates captured-revision deploys.
type Service struct {
	deps   Deps
	log    zerowrap.Logger
	probes *ProbeDeps

	// coord serializes workload mutations per app: deploy/start/
	// restart/stop/remove/boot lock blocking, periodic reconciliation
	// skips busy apps.
	coord *appCoordinator
	// barrier is the process-wide GC barrier. A shared lease covers
	// every workload mutation from resource selection through durable
	// protection publication; prune holds the exclusive lease.
	barrier out.GCBarrier
	// backoff is the in-memory, generation-scoped recovery budget.
	backoff *recoveryBackoff
	// publication holds the in-memory publication inhibition: a
	// service whose fail-closed withdrawal could not be applied must be
	// withdrawn again before any later publication.
	publication publicationInhibition
	// executions remembers the last observed execution start per
	// generation, so a native restart is detected without restarting
	// the workload ourselves.
	executions executionTracker
}

// NewService creates the deployment engine. All deps are required;
// Secrets may be nil only in tests that never reach secret preflight.
func NewService(deps Deps, log zerowrap.Logger) *Service {
	return &Service{
		deps:    deps,
		log:     log,
		coord:   newAppCoordinator(),
		backoff: newRecoveryBackoff(time.Now),
	}
}

// WithGCBarrier wires the process-wide GC barrier. Every workload
// mutation holds a shared lease from resource selection through the
// durable publication of the resource's protection, so prune can never
// observe a half-acquired resource. Nil is valid for tests that never
// run prune.
func (s *Service) WithGCBarrier(barrier out.GCBarrier) *Service {
	s.barrier = barrier
	return s
}

// acquireAppContext takes the GC shared lease first and the per-app
// coordinator second, following the documented lock order
// (GC barrier → per-app coordinator → registry lock → bbolt
// transaction). The returned release drops both, coordinator first.
func (s *Service) acquireAppContext(ctx context.Context, app string) (func(), error) {
	lease, err := s.acquireSharedGC(ctx)
	if err != nil {
		return nil, err
	}
	release, err := s.coord.acquire(ctx, app)
	if err != nil {
		lease.Release()
		return nil, err
	}
	return func() {
		release()
		lease.Release()
	}, nil
}

// tryAcquireAppContext takes the GC shared lease, then tries the per-app
// coordinator without blocking. ok is false when the app is busy; the
// caller skips it. Periodic reconciliation still waits for an in-flight
// prune, because a half-planned snapshot is exactly what must not be
// raced.
func (s *Service) tryAcquireAppContext(ctx context.Context, app string) (func(), bool) {
	lease, err := s.acquireSharedGC(ctx)
	if err != nil {
		return nil, false
	}
	release, ok := s.coord.tryAcquire(app)
	if !ok {
		lease.Release()
		return nil, false
	}
	return func() {
		release()
		lease.Release()
	}, true
}

// acquireSharedGC takes a shared GC lease, or a no-op lease when no
// barrier is wired.
func (s *Service) acquireSharedGC(ctx context.Context) (out.GCLease, error) {
	if s.barrier == nil {
		return noopGCLease{}, nil
	}
	return s.barrier.AcquireShared(ctx)
}

// noopGCLease is the lease used when no barrier is wired.
type noopGCLease struct{}

func (noopGCLease) Release() {}

// WithProbeDeps overrides readiness probing (tests inject a mock-backed
// ProbeDeps via NewProbeDeps; production leaves it nil for the live
// runtime adapter).
func (s *Service) WithProbeDeps(probes ProbeDeps) *Service {
	s.probes = &probes
	return s
}

// DeployInput selects the revision and service scope.
type DeployInput struct {
	App      string
	Revision string
	Service  string
	Op       string
}

// DeployResult carries terminal per-service results and cleanup warnings.
type DeployResult struct {
	Op              string
	App             string
	Revision        string
	Outcome         string
	Services        map[string]ServiceResult
	CleanupWarnings []CleanupWarning
	Interrupted     []string
	// Removed names services retired because the new revision no longer
	// declares them.
	Removed []string
}

// ServiceResult is one service's terminal deployment result.
type ServiceResult struct {
	Result            string
	EffectiveRevision string
	Before            string
	After             string
	RestartUnsafe     bool
	Error             string
	// BackendBinds carries the created container's TCP loopback publishes
	// (container port -> 127.0.0.1 host port) for the active record.
	BackendBinds map[int]int
	// UDPBackendBinds carries the created container's UDP loopback
	// publishes for the active record. Nil when the service declares
	// no UDP interface.
	UDPBackendBinds map[int]int
	// Retire is the exact container ID to stop+remove AFTER the new
	// effective state is published. Empty when nothing retires.
	Retire string
	// Diagnostics is bounded, redacted failure output of the failed
	// candidate. It is never embedded in Error and is persisted to the
	// journal for logs-scoped retrieval only.
	Diagnostics []string
}

// CleanupWarning records post-publication leftovers for operator action.
type CleanupWarning struct {
	Service  string
	Leftover string
	Detail   string
}

// pinnedService binds a service spec to its resolved digest plus the
// captured revision's app-wide public env (injected into every service
// alongside that service's own resolved secrets). appEnv is a copy owned
// by this pin: never mutated, never persisted with secret values.
type pinnedService struct {
	name         string
	spec         domain.AppService
	digest       string
	runtimeImage string
	appEnv       map[string]string
	// sharedNetworks are the declared shared-network memberships this
	// service joins. The private incarnation network is derived from the
	// app UUID at create time so recovery and deploy agree.
	sharedNetworks []domain.AppSharedNetwork
}

// newOpID allocates a time-ordered op- identifier (uuid v7).
func newOpID() string {
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	var buf [32]byte
	hex.Encode(buf[:], id[:])
	return "op-" + string(buf[:])
}

// Preflight resolves a captured revision without any workload mutation:
// image digests, secret presence, image-volume mapping, reservation
// recheck, and resource preconditions. It records the pinned digest
// table into the journal BEFORE any effect.
func (s *Service) Preflight(ctx context.Context, input DeployInput) ([]pinnedService, *domain.AppOperation, error) {
	release, err := s.acquireAppContext(ctx, input.App)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	return s.preflightLocked(ctx, input)
}

func (s *Service) preflightLocked(ctx context.Context, input DeployInput) ([]pinnedService, *domain.AppOperation, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Preflight",
		"app":                 input.App,
	})
	log := zerowrap.FromCtx(ctx)

	if err := s.deps.State.Recover(ctx); err != nil {
		return nil, nil, fmt.Errorf("deployment: recover before preflight: %w", err)
	}
	rev, err := s.resolveRevision(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	if input.Service != "" {
		if err := s.checkConverged(ctx, input.App, rev); err != nil {
			return nil, nil, err
		}
	}

	opID := input.Op
	if opID == "" {
		opID = newOpID()
	}
	op := &domain.AppOperation{
		Op:            opID,
		Kind:          "deploy",
		App:           input.App,
		InputRevision: rev.Revision,
		StartedAt:     time.Now().UTC(),
		Steps:         []domain.AppOperationStep{{ID: "preflight", State: domain.AppStepPending}},
	}
	if err := s.deps.State.SaveOperation(ctx, *op); err != nil {
		return nil, nil, fmt.Errorf("deployment: persist journal before effects: %w", err)
	}

	pinned, err := s.preflightServices(ctx, input.App, rev, input.Service)
	if err != nil {
		op.Steps[0] = domain.AppOperationStep{ID: "preflight", State: domain.AppStepFailed, Error: err.Error()}
		op.Outcome = domain.AppOutcomeFailed
		if saveErr := s.deps.State.SaveOperation(ctx, *op); saveErr != nil {
			log.Warn().Err(saveErr).Msg("deployment: failed to record preflight failure")
		}
		return nil, op, err
	}
	op.Steps[0] = domain.AppOperationStep{ID: "preflight", State: domain.AppStepSucceeded}
	for _, p := range pinned {
		op.Steps = append(op.Steps, domain.AppOperationStep{
			ID:      "service." + p.name + ".replace",
			State:   domain.AppStepPending,
			Service: p.name,
			Digest:  p.digest,
			Image:   p.runtimeImage,
		})
	}
	if err := s.deps.State.SaveOperation(ctx, *op); err != nil {
		return nil, nil, fmt.Errorf("deployment: persist pinned table: %w", err)
	}
	log.Info().Str("op", opID).Str("revision", rev.Revision).Int("services", len(pinned)).Msg("deployment: preflight passed")
	return pinned, op, nil
}

// resolveRevision loads the captured revision (default: current desired).
func (s *Service) resolveRevision(ctx context.Context, input DeployInput) (domain.AppDesiredRevision, error) {
	if input.Revision != "" {
		rev, err := s.deps.State.LoadRevision(ctx, input.App, input.Revision)
		if err != nil {
			return domain.AppDesiredRevision{}, err
		}
		return rev, nil
	}
	rev, ok, err := s.deps.State.LoadDesired(ctx, input.App)
	if err != nil {
		return domain.AppDesiredRevision{}, err
	}
	if !ok {
		return domain.AppDesiredRevision{}, fmt.Errorf("deployment: app %q has no desired state: %w", input.App, domain.ErrAppRevisionNotFound)
	}
	return rev, nil
}

// checkConverged refuses service-targeted deploy on desired/effective divergence.
func (s *Service) checkConverged(ctx context.Context, app string, rev domain.AppDesiredRevision) error {
	active, ok, err := s.deps.State.LoadActive(ctx, app)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("deployment: app %q was never deployed, targeted deploy refused: %w", app, domain.ErrAppStateConflict)
	}
	if !active.Converged || active.ConvergedRevision != rev.Revision {
		return fmt.Errorf(
			"%w: service-targeted deploy refused, desired %s diverges from effective state",
			domain.ErrAppStateConflict, rev.Revision,
		)
	}
	return nil
}

// preflightServices runs the five preflight gates in order. No mutation.
func (s *Service) preflightServices(ctx context.Context, app string, rev domain.AppDesiredRevision, onlyService string) ([]pinnedService, error) {
	services := append([]domain.AppService(nil), rev.Spec.Services...)
	if onlyService != "" {
		filtered := services[:0]
		for _, svc := range services {
			if svc.Name == onlyService {
				filtered = append(filtered, svc)
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("deployment: service %q not in revision %s: %w", onlyService, rev.Revision, domain.ErrAppStateConflict)
		}
		services = filtered
	}
	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })

	var pinned []pinnedService
	ownership, err := s.deps.State.LoadOwnership(ctx, app)
	if err != nil {
		return nil, err
	}
	// Defensive re-check: stored revisions predate or bypass apply
	// validation; an overlap must fail preflight before any mutation.
	if err := rev.Spec.CheckEnvSecretCollisions(); err != nil {
		return nil, err
	}
	appEnv := maps.Clone(rev.Spec.Env)
	if appEnv == nil {
		appEnv = map[string]string{}
	}
	for _, svc := range services {
		digest, err := s.deps.Images.ResolveDigest(ctx, svc.Image)
		if err != nil {
			return nil, fmt.Errorf("deployment: image %q unresolvable: %w", svc.Image, domain.ErrAppImageUnresolvable)
		}
		if err := s.checkSecrets(ctx, app, ownership.ID, svc); err != nil {
			return nil, err
		}
		runtimeImage, err := s.preflightImage(ctx, svc.Image, digest)
		if err != nil {
			return nil, err
		}
		if err := s.checkImageVolumes(ctx, svc, runtimeImage); err != nil {
			return nil, err
		}
		pinned = append(pinned, pinnedService{
			name: svc.Name, spec: svc, digest: digest, runtimeImage: runtimeImage, appEnv: appEnv,
			sharedNetworks: domain.AppServiceSharedNetworks(rev.Spec, svc.Name),
		})
	}
	if err := s.recheckReservations(ctx, app, rev); err != nil {
		return nil, err
	}
	if err := s.checkResources(ctx, app, ownership, services); err != nil {
		return nil, err
	}
	return pinned, nil
}

// checkSecrets reads every required secret path once. Values stay in
// memory for container creation only; nothing is written anywhere.
// Paths are UUID-keyed so a removed app's secrets are never adopted
// by a new app reusing the name.
func (s *Service) checkSecrets(ctx context.Context, app, appID string, svc domain.AppService) error {
	if s.deps.Secrets == nil {
		return fmt.Errorf("deployment: secret provider unavailable: %w", domain.ErrAppSecretMissing)
	}
	for envKey, name := range svc.Secrets {
		path := domain.AppSecretPathForID(appID, app, svc.Name, name)
		if _, err := s.deps.Secrets.GetSecret(ctx, path); err != nil {
			return fmt.Errorf("deployment: secret %s (env %s) missing: %w", path, envKey, domain.ErrAppSecretMissing)
		}
	}
	return nil
}

// appSecretID returns the stable internal UUID for secret paths.
// Empty means legacy name-keyed paths (records predating UUIDs).
// Used on the execution path where preflight's ownership snapshot is
// not in scope; deploys are infrequent, one extra store read per
// service is acceptable.
func (s *Service) appSecretID(ctx context.Context, app string) (string, error) {
	ownership, err := s.deps.State.LoadOwnership(ctx, app)
	if err != nil {
		return "", err
	}
	return ownership.ID, nil
}

func (s *Service) preflightImage(ctx context.Context, image, digest string) (string, error) {
	if err := s.validateImageSource(image, digest); err != nil {
		return "", err
	}
	if s.deps.Registry.Domain == "" {
		return image, nil
	}
	return s.pullImage(ctx, stripImageTag(image)+"@"+digest)
}

// validateImageSource enforces the installation image policy on a
// reference before any pull. Both the manifest reference and the pinned
// digest are checked, so a digest-pinned ref cannot bypass the hostname
// and port allowlist on deploy or recovery.
func (s *Service) validateImageSource(image, digest string) error {
	ref := strings.TrimSpace(image)
	if digest != "" && !strings.Contains(ref, "@") {
		ref = stripImageTag(image) + "@" + digest
	}
	if err := s.deps.ImagePolicy.ValidateImageSource(ref); err != nil {
		return fmt.Errorf("deployment: image %q: %w", image, err)
	}
	return nil
}

// checkImageVolumes rejects images with unmapped VOLUME declarations.
func (s *Service) checkImageVolumes(ctx context.Context, svc domain.AppService, runtimeImage string) error {
	declared, err := s.deps.Runtime.InspectImageVolumes(ctx, runtimeImage)
	if err != nil {
		return fmt.Errorf("deployment: inspect image volumes: %w", err)
	}
	if len(declared) == 0 {
		return nil
	}
	mapped := map[string]struct{}{}
	for _, vol := range svc.Volumes {
		mapped[vol.Path] = struct{}{}
	}
	for _, path := range declared {
		if _, ok := mapped[path]; !ok {
			return fmt.Errorf(
				"deployment: image declares unmanaged volume %q, map it explicitly: %w",
				path, domain.ErrAppUnmanagedImageVolume,
			)
		}
	}
	return nil
}

// recheckReservations re-validates against the live global table.
func (s *Service) recheckReservations(ctx context.Context, app string, rev domain.AppDesiredRevision) error {
	checkpoint, err := s.deps.State.LoadCheckpoint(ctx)
	if err != nil {
		return err
	}
	reservations := domain.ReservationsFor(rev.Spec)
	if err := domain.CheckReservations(checkpoint.Reservations, reservations, app); err != nil {
		return fmt.Errorf("deployment: %w", err)
	}
	return nil
}

// checkResources verifies volumes exist-or-creatable and ownership is consistent.
// A generated runtime volume that already exists but is NOT recorded in
// this app's ownership is refused fail-closed: mounting it would
// implicitly adopt (and possibly modify) foreign data. Volumes recorded
// in ownership (attached or retained) were created by this app's own
// deploys and are safe to reuse.
func (s *Service) checkResources(ctx context.Context, app string, ownership domain.AppOwnership, services []domain.AppService) error {
	owned := map[string]string{}
	ownedRuntime := map[string]struct{}{}
	for _, vol := range ownership.Volumes {
		owned[vol.Name] = vol.Service
		ownedRuntime[vol.RuntimeName] = struct{}{}
	}
	for _, svc := range services {
		for _, vol := range svc.Volumes {
			if owner, ok := owned[vol.Name]; ok && owner != svc.Name {
				return fmt.Errorf(
					"deployment: volume %q owned by %q, not %q: %w",
					vol.Name, owner, svc.Name, domain.ErrAppStateConflict,
				)
			}
			runtimeName := domain.RuntimeVolumeName(app, svc.Name, vol.Name)
			exists, err := s.deps.Runtime.VolumeExists(ctx, runtimeName)
			if err != nil {
				return fmt.Errorf("deployment: check volume %q: %w", runtimeName, err)
			}
			if exists {
				if _, ok := ownedRuntime[runtimeName]; !ok {
					return fmt.Errorf(
						"deployment: runtime volume %q already exists but is not owned by app %q: refusing to adopt foreign data: %w",
						runtimeName, app, domain.ErrAppStateConflict,
					)
				}
			}
		}
	}
	return nil
}

// ComputeOutcome derives the op outcome over terminal per-service results.
func ComputeOutcome(results map[string]ServiceResult) string {
	deployed := 0
	failed := 0
	for _, result := range results {
		switch result.Result {
		case "deployed":
			deployed++
		case "failed":
			failed++
		}
	}
	switch {
	case deployed > 0 && failed > 0:
		return domain.AppOutcomePartial
	case deployed > 0:
		return domain.AppOutcomeSuccess
	default:
		return domain.AppOutcomeFailed
	}
}

// itoa formats integers without extra imports.
func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
