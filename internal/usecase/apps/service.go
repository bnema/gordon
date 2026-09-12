// Package apps implements the desired-state use case: manifest apply,
// dry-run validation, desired/active inspection, and reservation
// management. It performs zero workload, pull, or secret-value effects;
// all persistence goes through the out.AppState boundary.
package apps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/google/uuid"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// Service orchestrates app desired-state transitions.
type Service struct {
	store     out.AppState
	log       zerowrap.Logger
	listeners map[string]domain.EntryPointListener
	// barrier is the process-wide GC barrier. An apply holds the shared
	// lease from validation through durable publication, so prune can
	// never snapshot between a staged intent and its materialization.
	barrier     out.GCBarrier
	imagePolicy domain.ImageSourcePolicy
}

// NewService creates the apps use case over an AppState store.
func NewService(store out.AppState, log zerowrap.Logger) *Service {
	return &Service{store: store, log: log}
}

// WithGCBarrier wires the process-wide GC barrier. Nil is valid for tests
// that never run prune.
func (s *Service) WithGCBarrier(barrier out.GCBarrier) *Service {
	s.barrier = barrier
	return s
}

// WithImagePolicy supplies the installation registry policy enforced while
// validating manifests, before any desired state is persisted.
func (s *Service) WithImagePolicy(policy domain.ImageSourcePolicy) *Service {
	s.imagePolicy = policy
	return s
}

// noopLease is a GC lease for an unwired barrier.
type noopLease struct{}

func (noopLease) Release() {}

// acquireSharedLease takes the shared GC lease for one apply. It fails
// closed: an apply must not publish desired state while prune holds the
// exclusive lease.
func (s *Service) acquireSharedLease(ctx context.Context) (out.GCLease, error) {
	if s.barrier == nil {
		return noopLease{}, nil
	}
	lease, err := s.barrier.AcquireShared(ctx)
	if err != nil {
		return nil, fmt.Errorf("apps: acquire GC lease: %w", err)
	}
	return lease, nil
}

// WithEntrypoints supplies the installation entrypoint listeners so Apply
// can reject an L4 publish declaration that does not match the listener
// that will actually bind it. A nil map leaves the check to projection.
func (s *Service) WithEntrypoints(listeners map[string]domain.EntryPointListener) *Service {
	s.listeners = listeners
	return s
}

// ApplyResult describes one accepted (or no-op) apply.
type ApplyResult struct {
	App               string
	FormerRevision    string
	ResultingRevision string
	Noop              bool
	Pending           bool
	Diff              domain.AppDiff
	IntentID          string
}

// DryRunResult describes validation without persistence.
type DryRunResult struct {
	App   string
	Valid bool
	Diff  domain.AppDiff
}

// newRevisionID allocates a time-ordered rev- identifier (uuid v7).
func newRevisionID() string {
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	return "rev-" + hexNoDashes(id)
}

// newIntentID allocates a time-ordered apply- identifier (uuid v7).
func newIntentID() string {
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	return "apply-" + hexNoDashes(id)
}

// hexNoDashes renders a UUID without dashes (32 hex chars).
func hexNoDashes(id uuid.UUID) string {
	var buf [32]byte
	hex.Encode(buf[:], id[:])
	return string(buf[:])
}

// Apply validates a normalized spec and persists it as desired state.
// source is the raw manifest bytes (hashed for audit, never re-read).
// dryRun validates and previews without persistence or effects.
func (s *Service) Apply(ctx context.Context, spec domain.AppSpec, source []byte, dryRun bool) (*ApplyResult, *DryRunResult, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Apply",
		"app":                 spec.Name,
		"dry_run":             dryRun,
	})
	log := zerowrap.FromCtx(ctx)

	if err := spec.Validate(); err != nil {
		return nil, nil, err
	}
	for _, service := range spec.Services {
		if err := s.imagePolicy.ValidateImageSource(service.Image); err != nil {
			return nil, nil, fmt.Errorf("apps: service %q image %q: %w", service.Name, service.Image, err)
		}
	}
	if err := s.validateEntrypointCompatibility(spec); err != nil {
		return nil, nil, err
	}
	// Hold the shared GC lease from validation through publication: prune
	// must never observe a staged-but-unmaterialized apply.
	lease, err := s.acquireSharedLease(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer lease.Release()
	prepared, err := s.prepareApply(ctx, spec)
	if err != nil {
		return nil, nil, err
	}
	if prepared.noop {
		log.Info().Str("revision", prepared.desired.Revision).Msg("apps: no-change apply, idempotent")
		return &ApplyResult{
			App:               spec.Name,
			FormerRevision:    prepared.desired.Revision,
			ResultingRevision: prepared.desired.Revision,
			Noop:              true,
			Diff:              prepared.diff,
		}, nil, nil
	}
	if dryRun {
		return nil, &DryRunResult{
			App:   spec.Name,
			Valid: true,
			Diff:  prepared.diff,
		}, nil
	}
	return s.persistApply(ctx, spec, source, prepared)
}

// validateEntrypointCompatibility checks every declared TCP/UDP interface
// against the installation entrypoint listeners before any persistence or
// workload effect. The runtime binds the entrypoint address, so a publish
// bind that differs from it (for example loopback) must be rejected
// rather than silently exposed on the public listener.
func (s *Service) validateEntrypointCompatibility(spec domain.AppSpec) error {
	if s.listeners == nil {
		return nil
	}
	for _, svc := range spec.Services {
		for _, t := range svc.TCP {
			if err := s.checkPublish(svc.Name, "tcp", t.Entrypoint, t.Publish, domain.NetworkProtocolTCP); err != nil {
				return err
			}
		}
		for _, u := range svc.UDP {
			if err := s.checkPublish(svc.Name, "udp", u.Entrypoint, u.Publish, domain.NetworkProtocolUDP); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) checkPublish(service, kind, entrypoint, publish string, transport domain.NetworkProtocol) error {
	listener, ok := s.listeners[entrypoint]
	if !ok {
		return fmt.Errorf("%w: service %q %s interface references unknown entrypoint %q", domain.ErrInvalidAppSpec, service, kind, entrypoint)
	}
	if err := domain.ValidatePublishForListener(publish, transport, listener, entrypoint); err != nil {
		return fmt.Errorf("apps: service %q: %w", service, err)
	}
	return nil
}

// applyPrepared carries validated apply inputs through to persistence.
type applyPrepared struct {
	desired      domain.AppDesiredRevision
	diff         domain.AppDiff
	reservations []domain.AppListenerReservation
	noop         bool
}

// prepareApply validates, recovers, checks conflicts, detects no-ops.
// It performs no writes.
func (s *Service) prepareApply(ctx context.Context, spec domain.AppSpec) (*applyPrepared, error) {
	if err := s.store.Recover(ctx); err != nil {
		return nil, fmt.Errorf("apps: recover before mutation: %w", err)
	}
	checkpoint, err := s.store.LoadCheckpoint(ctx)
	if err != nil {
		return nil, err
	}
	reservations := domain.ReservationsFor(spec)
	desired, hasDesired, err := s.store.LoadDesired(ctx, spec.Name)
	if err != nil {
		return nil, err
	}
	active, _, err := s.store.LoadActive(ctx, spec.Name)
	if err != nil {
		return nil, err
	}
	if err := domain.CheckReservations(checkpoint.Reservations, reservations, spec.Name); err != nil {
		return nil, err
	}
	diff := domain.DiffAppSpec(spec, activeSpec(active))
	if hasDesired && specsEqual(desired.Spec, spec) {
		return &applyPrepared{desired: desired, diff: diff, reservations: reservations, noop: true}, nil
	}
	return &applyPrepared{desired: desired, diff: diff, reservations: reservations}, nil
}

// persistApply stages, commits, and materializes one accepted apply.
func (s *Service) persistApply(ctx context.Context, spec domain.AppSpec, source []byte, prepared *applyPrepared) (*ApplyResult, *DryRunResult, error) {
	log := zerowrap.FromCtx(ctx)
	sourceHash := sha256.Sum256(source)
	supersedes := prepared.desired.Revision
	intent := domain.AppApplyIntent{
		Intent:       newIntentID(),
		App:          spec.Name,
		Revision:     newRevisionID(),
		Supersedes:   supersedes,
		SourceSHA256: hex.EncodeToString(sourceHash[:]),
		Spec:         spec,
		Reservations: prepared.reservations,
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.store.StageApply(ctx, intent); err != nil {
		return nil, nil, fmt.Errorf("apps: stage apply: %w", err)
	}
	if err := s.store.CommitApply(ctx, spec.Name, intent.Intent); err != nil {
		return nil, nil, fmt.Errorf("apps: commit apply: %w", err)
	}
	if err := s.store.MaterializeApply(ctx, spec.Name, intent.Intent); err != nil {
		return nil, nil, fmt.Errorf("apps: materialize apply (re-query by intent): %w", err)
	}
	if err := s.store.CollectGarbage(ctx, spec.Name, []string{intent.Intent}); err != nil {
		log.Warn().Err(err).Msg("apps: garbage collection failed, continuing")
	}
	log.Info().Str("revision", intent.Revision).Str("intent", intent.Intent).Msg("apps: apply accepted")
	return &ApplyResult{
		App:               spec.Name,
		FormerRevision:    supersedes,
		ResultingRevision: intent.Revision,
		Pending:           true,
		Diff:              prepared.diff,
		IntentID:          intent.Intent,
	}, nil, nil
}

// activeSpec rebuilds an AppSpec view from effective definitions for diffing.
func activeSpec(active domain.AppActive) domain.AppSpec {
	spec := domain.AppSpec{Name: active.App, Env: map[string]string{}}
	for name, svc := range active.Services {
		svcSpec := svc.Spec
		svcSpec.Name = name
		spec.Services = append(spec.Services, svcSpec)
	}
	return spec
}

// specsEqual compares normalized specs for no-op detection.
func specsEqual(a, b domain.AppSpec) bool {
	diff := domain.DiffAppSpec(a, b)
	return len(diff.Added) == 0 && len(diff.Removed) == 0 && len(diff.Changed) == 0
}
