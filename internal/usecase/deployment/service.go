// Package deployment implements the app deployment engine: preflight,
// journaled execution, and terminal-result computation. It is
// preparation work for the cutover gate: no wiring into the daemon,
// events, or traffic manager. Frozen by
// docs/plans/v2.50.0/03-deployment.md.
package deployment

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/google/uuid"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apps"
)

// Deps are the driven adapters owned by the deployment engine.
type Deps struct {
	State   out.AppState
	Runtime out.ContainerRuntime
	Images  out.ImageResolver
	Secrets out.SecretProvider
	Apps    *apps.Service
}

// Service orchestrates captured-revision deploys.
type Service struct {
	deps Deps
	log  zerowrap.Logger
}

// NewService creates the deployment engine. All deps are required;
// Secrets may be nil only in tests that never reach secret preflight.
func NewService(deps Deps, log zerowrap.Logger) *Service {
	return &Service{deps: deps, log: log}
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
}

// ServiceResult is one service's terminal deployment result.
type ServiceResult struct {
	Result            string
	EffectiveRevision string
	Before            string
	After             string
	RestartUnsafe     bool
	Error             string
}

// CleanupWarning records post-publication leftovers for operator action.
type CleanupWarning struct {
	Service  string
	Leftover string
	Detail   string
}

// pinnedService binds a service spec to its resolved digest.
type pinnedService struct {
	name   string
	spec   domain.AppService
	digest string
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
			ID:     "service." + p.name + ".replace",
			State:  domain.AppStepPending,
			Detail: "digest=" + p.digest,
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
	for _, svc := range services {
		digest, err := s.deps.Images.ResolveDigest(ctx, svc.Image)
		if err != nil {
			return nil, fmt.Errorf("deployment: image %q unresolvable: %w", svc.Image, domain.ErrAppImageUnresolvable)
		}
		if err := s.checkSecrets(ctx, app, svc); err != nil {
			return nil, err
		}
		if err := s.checkImageVolumes(ctx, svc); err != nil {
			return nil, err
		}
		pinned = append(pinned, pinnedService{name: svc.Name, spec: svc, digest: digest})
	}
	if err := s.recheckReservations(ctx, app, rev); err != nil {
		return nil, err
	}
	if err := s.checkResources(ctx, app, services); err != nil {
		return nil, err
	}
	return pinned, nil
}

// checkSecrets reads every required secret path once. Values stay in
// memory for container creation only; nothing is written anywhere.
func (s *Service) checkSecrets(ctx context.Context, app string, svc domain.AppService) error {
	if s.deps.Secrets == nil {
		return fmt.Errorf("deployment: secret provider unavailable: %w", domain.ErrAppSecretMissing)
	}
	for envKey, name := range svc.Secrets {
		path := domain.AppSecretPath(app, svc.Name, name)
		if _, err := s.deps.Secrets.GetSecret(ctx, path); err != nil {
			return fmt.Errorf("deployment: secret %s (env %s) missing: %w", path, envKey, domain.ErrAppSecretMissing)
		}
	}
	return nil
}

// checkImageVolumes rejects images with unmapped VOLUME declarations.
func (s *Service) checkImageVolumes(ctx context.Context, svc domain.AppService) error {
	declared, err := s.deps.Runtime.InspectImageVolumes(ctx, svc.Image)
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
	reservations := apps.ReservationsFor(rev.Spec)
	seen := map[string]string{}
	keyOf := func(res domain.AppListenerReservation) string {
		if res.Proto == "http" {
			return "http://" + res.Host
		}
		return res.Proto + "://" + res.IP + ":" + itoa(res.Port)
	}
	for _, res := range reservations {
		key := keyOf(res)
		if prev, ok := seen[key]; ok {
			return fmt.Errorf("%w: %s claims %s twice (%s vs %s)", domain.ErrAppReservationConflict, app, key, prev, res.Service)
		}
		seen[key] = res.Service
	}
	for _, res := range reservations {
		for _, other := range checkpoint.Reservations {
			if other.App == app {
				continue
			}
			if reservationOverlaps(res, other) {
				return fmt.Errorf("%w: %s/%s conflicts with %s/%s", domain.ErrAppReservationConflict, app, res.Service, other.App, other.Service)
			}
		}
	}
	return nil
}

// reservationOverlaps mirrors the apps use-case overlap rules.
func reservationOverlaps(a, b domain.AppListenerReservation) bool {
	if a.Proto != b.Proto {
		return false
	}
	if a.Proto == "http" {
		return a.Host == b.Host
	}
	if a.Port != b.Port {
		return false
	}
	return isWildcardIP(a.IP) || isWildcardIP(b.IP) || a.IP == b.IP
}

// isWildcardIP matches wildcard and dual-family entries.
func isWildcardIP(ip string) bool {
	return ip == "" || ip == "0.0.0.0" || ip == "::" || ip == "dual"
}

// checkResources verifies volumes exist-or-creatable and ownership is consistent.
func (s *Service) checkResources(ctx context.Context, app string, services []domain.AppService) error {
	ownership, err := s.deps.State.LoadOwnership(ctx, app)
	if err != nil {
		return err
	}
	owned := map[string]string{}
	for _, vol := range ownership.Volumes {
		owned[vol.Name] = vol.Service
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
			_ = exists
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
