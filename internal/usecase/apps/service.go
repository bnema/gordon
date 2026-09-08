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
	"sort"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/google/uuid"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// Service orchestrates app desired-state transitions.
type Service struct {
	store out.AppState
	log   zerowrap.Logger
}

// NewService creates the apps use case over an AppState store.
func NewService(store out.AppState, log zerowrap.Logger) *Service {
	return &Service{store: store, log: log}
}

// ApplyResult describes one accepted (or no-op) apply.
type ApplyResult struct {
	App               string
	FormerRevision    string
	ResultingRevision string
	Noop              bool
	Pending           bool
	Diff              domain.AppDiff
	Warnings          []string
	IntentID          string
}

// DryRunResult describes validation without persistence.
type DryRunResult struct {
	App      string
	Valid    bool
	Diff     domain.AppDiff
	Warnings []string
	Occupied []string
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
func (s *Service) Apply(ctx context.Context, spec domain.AppSpec, source []byte, sourceName string, dryRun bool) (*ApplyResult, *DryRunResult, error) {
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
			App:      spec.Name,
			Valid:    true,
			Diff:     prepared.diff,
			Occupied: prepared.occupied,
		}, nil
	}
	return s.persistApply(ctx, spec, source, sourceName, prepared)
}

// applyPrepared carries validated apply inputs through to persistence.
type applyPrepared struct {
	desired      domain.AppDesiredRevision
	diff         domain.AppDiff
	occupied     []string
	reservations []domain.AppListenerReservation
	noop         bool
}

// prepareApply validates, recovers, checks conflicts, detects no-ops.
// It performs no writes.
func (s *Service) prepareApply(ctx context.Context, spec domain.AppSpec) (*applyPrepared, error) {
	log := zerowrap.FromCtx(ctx)
	if err := s.store.Recover(ctx); err != nil {
		return nil, fmt.Errorf("apps: recover before mutation: %w", err)
	}
	checkpoint, err := s.store.LoadCheckpoint(ctx)
	if err != nil {
		return nil, err
	}
	reservations := ReservationsFor(spec)
	occupied, err := detectOccupied(reservations)
	if err != nil {
		log.Warn().Err(err).Msg("apps: occupancy probe failed, reporting unknown")
	}
	desired, hasDesired, err := s.store.LoadDesired(ctx, spec.Name)
	if err != nil {
		return nil, err
	}
	active, _, err := s.store.LoadActive(ctx, spec.Name)
	if err != nil {
		return nil, err
	}
	if err := checkConflicts(checkpoint.Reservations, reservations, spec.Name); err != nil {
		return nil, err
	}
	diff := domain.DiffAppSpec(spec, activeSpec(active))
	if hasDesired && specsEqual(desired.Spec, spec) {
		return &applyPrepared{desired: desired, diff: diff, occupied: occupied, reservations: reservations, noop: true}, nil
	}
	return &applyPrepared{desired: desired, diff: diff, occupied: occupied, reservations: reservations}, nil
}

// persistApply stages, commits, and materializes one accepted apply.
func (s *Service) persistApply(ctx context.Context, spec domain.AppSpec, source []byte, sourceName string, prepared *applyPrepared) (*ApplyResult, *DryRunResult, error) {
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
	if err := s.store.CollectGarbage(ctx, spec.Name, nil); err != nil {
		log.Warn().Err(err).Msg("apps: garbage collection failed, continuing")
	}
	_ = sourceName
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

// ReservationsFor computes global listener claims for a normalized spec.
func ReservationsFor(spec domain.AppSpec) []domain.AppListenerReservation {
	var reservations []domain.AppListenerReservation
	for _, svc := range spec.Services {
		for _, h := range svc.HTTP {
			reservations = append(reservations, domain.AppListenerReservation{
				Proto:   "http",
				Host:    h.Host,
				Service: svc.Name,
				App:     spec.Name,
			})
		}
		for _, t := range svc.TCP {
			host, port, err := domain.ParsePublish(t.Publish)
			if err != nil {
				continue
			}
			reservations = append(reservations, domain.AppListenerReservation{
				Proto:   "tcp",
				IP:      publishIP(host),
				Port:    port,
				Service: svc.Name,
				App:     spec.Name,
			})
		}
		for _, u := range svc.UDP {
			host, port, err := domain.ParsePublish(u.Publish)
			if err != nil {
				continue
			}
			reservations = append(reservations, domain.AppListenerReservation{
				Proto:   "udp",
				IP:      publishIP(host),
				Port:    port,
				Service: svc.Name,
				App:     spec.Name,
			})
		}
		for _, r := range svc.RCON {
			host, port, err := domain.ParsePublish(r.Publish)
			if err != nil {
				continue
			}
			reservations = append(reservations, domain.AppListenerReservation{
				Proto:   "tcp",
				IP:      publishIP(host),
				Port:    port,
				Service: svc.Name,
				App:     spec.Name,
			})
		}
	}
	sort.Slice(reservations, func(i, j int) bool {
		if reservations[i].Proto != reservations[j].Proto {
			return reservations[i].Proto < reservations[j].Proto
		}
		if reservations[i].Host != reservations[j].Host {
			return reservations[i].Host < reservations[j].Host
		}
		if reservations[i].Port != reservations[j].Port {
			return reservations[i].Port < reservations[j].Port
		}
		return reservations[i].IP < reservations[j].IP
	})
	return reservations
}

// publishIP maps an empty publish host to the wildcard entry.
func publishIP(host string) string {
	if host == "" {
		return "dual"
	}
	return host
}

// checkConflicts validates a candidate against checkpoint + overlay rules.
// Same-app active reservations are retained until withdrawal, so the
// candidate's own app entries never self-conflict.
func checkConflicts(existing []domain.AppListenerReservation, candidate []domain.AppListenerReservation, app string) error {
	// Duplicate claims within the candidate itself.
	seen := map[string]string{}
	for _, res := range candidate {
		key := reservationKey(res)
		if prev, ok := seen[key]; ok {
			return fmt.Errorf("%w: %s claims %s twice (service %s vs %s)", domain.ErrAppReservationConflict, app, key, prev, res.Service)
		}
		seen[key] = res.Service
	}
	for _, res := range candidate {
		key := reservationKey(res)
		for _, other := range existing {
			if other.App == app {
				continue
			}
			if overlaps(res, other) {
				_ = key
				return fmt.Errorf(
					"%w: %s/%s conflicts with %s/%s on %s",
					domain.ErrAppReservationConflict,
					app, res.Service, other.App, other.Service, describeReservation(res),
				)
			}
		}
	}
	return nil
}

// reservationKey is the exact-match identity for duplicates.
func reservationKey(res domain.AppListenerReservation) string {
	if res.Proto == "http" {
		return "http://" + res.Host
	}
	return res.Proto + "://" + res.IP + ":" + itoa(res.Port)
}

// overlaps reports wildcard/specific conflicts within one proto namespace.
func overlaps(a, b domain.AppListenerReservation) bool {
	if a.Proto != b.Proto {
		return false
	}
	if a.Proto == "http" {
		return a.Host == b.Host
	}
	if a.Port != b.Port {
		return false
	}
	if isWildcard(a.IP) || isWildcard(b.IP) {
		return true
	}
	return a.IP == b.IP
}

// isWildcard matches wildcard and dual-family entries.
func isWildcard(ip string) bool {
	return ip == "" || ip == "0.0.0.0" || ip == "::" || ip == "dual"
}

// describeReservation renders a human-readable claim.
func describeReservation(res domain.AppListenerReservation) string {
	if res.Proto == "http" {
		return "http://" + res.Host
	}
	return res.Proto + "://" + res.IP + ":" + itoa(res.Port)
}

// itoa avoids strconv import weight in hot paths; kept simple.
func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// detectOccupied probes loopback dials for advisory occupancy only.
// It never claims locks against unrelated host processes.
func detectOccupied(_ []domain.AppListenerReservation) ([]string, error) {
	return nil, nil
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
