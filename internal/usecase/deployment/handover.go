package deployment

import (
	"context"
	"fmt"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

const handoverStartSlack = 15 * time.Second

func (s *Service) handoverBudget(spec domain.AppService, grace time.Duration) time.Duration {
	return min(grace+readinessTimeout(spec)+handoverStartSlack, s.deps.Handover.MaxHold)
}

// holdingEnabled reports whether handovers hold traffic instead of
// withdrawing it.
func (s *Service) holdingEnabled() bool {
	return s.deps.Handover.MaxHold > 0 && s.deps.Traffic != nil
}

// beginHandover holds traffic, or withdraws it when holding is disabled. A
// failed graph apply can leave the hold registered, so a failure releases
// it before returning: held clients fail fast instead of waiting out the
// budget.
func (s *Service) beginHandover(ctx context.Context, app, name string, until time.Time) error {
	if !s.holdingEnabled() {
		return s.withdrawForRecovery(ctx, app, name)
	}
	if err := s.deps.Traffic.HoldService(ctx, app, name, until); err != nil {
		s.publication.mark(app, name)
		s.endHandover(ctx, app, name)
		return fmt.Errorf("deployment: hold %s/%s: %w", app, name, err)
	}
	s.publication.clear(app, name)
	return nil
}

func (s *Service) endHandover(ctx context.Context, app, name string) {
	if err := s.withdrawForRecovery(ctx, app, name); err != nil {
		log := zerowrap.FromCtx(ctx)
		log.Warn().Err(err).Str("app", app).Str("service", name).Msg("failed to end traffic handover")
	}
}

// failAfterHandover releases the hold, then removes the never-started
// candidate.
func (s *Service) failAfterHandover(ctx context.Context, app, revision, name, before, candidateID string, err error) ServiceResult {
	s.endHandover(ctx, app, name)
	return s.failPreparedCandidate(ctx, app, revision, name, before, candidateID, err)
}

// failPreparedCandidate removes a never-started candidate and keeps its ID
// in the failed result.
func (s *Service) failPreparedCandidate(ctx context.Context, app, revision, name, before, candidateID string, err error) ServiceResult {
	result := s.failResult(revision, before, candidateID, err)
	result.CleanupWarnings = append(result.CleanupWarnings, s.retireCandidate(ctx, app, name, candidateID)...)
	return result
}

func (s *Service) deployHandover(ctx context.Context, app, revision string, p pinnedService, opID, before string, grace time.Duration, journal candidateJournal) ServiceResult {
	prepared, err := s.prepareCandidate(ctx, app, revision, p, opID, journal)
	if err != nil {
		return s.candidateFailResult(revision, before, prepared.Container, err)
	}
	candidateID := prepared.Container.ID
	until := time.Now().Add(s.handoverBudget(p.spec, grace))
	if err := s.beginHandover(ctx, app, p.name, until); err != nil {
		// beginHandover already released its hold.
		return s.failPreparedCandidate(ctx, app, revision, p.name, before, candidateID, err)
	}
	if singleWriterRequired(p.spec) {
		if err := s.inhibitRecovery(ctx, app, p.name, before, domain.AppInhibitReplacementPending, opID); err != nil {
			return s.failAfterHandover(ctx, app, revision, p.name, before, candidateID, err)
		}
	}
	retired := s.retireContainer(ctx, app, retireOptions{Service: p.name, Grace: grace}, before)
	if !retired.Gone {
		return s.failAfterHandover(ctx, app, revision, p.name, before, candidateID, fmt.Errorf("deployment: retire superseded container %s: %s", before, cleanupDetail(retired)))
	}
	result := s.startPreparedCandidate(ctx, app, revision, p, before, prepared)
	if result.Result != "deployed" {
		s.endHandover(ctx, app, p.name)
	}
	return result
}

// endFailedHold releases a hold whose registration may have survived a
// failed graph apply. Disabled holding has nothing to release.
func (s *Service) endFailedHold(ctx context.Context, app, name string) {
	if s.holdingEnabled() {
		s.endHandover(ctx, app, name)
	}
}

// Disabled holding keeps the withdrawal ordering: the restart failure only
// clears the recorded binds.
func (s *Service) endRestartHandover(ctx context.Context, app, name string) {
	if !s.holdingEnabled() {
		s.clearServiceBinds(ctx, app, name)
		return
	}
	s.endHandover(ctx, app, name)
}
