package deployment

import "context"

// createAndStart prepares and starts one candidate, as the deploy paths do
// in two steps.
func (s *Service) createAndStart(ctx context.Context, app, revision string, p pinnedService, opID string, journal candidateJournal) (startedCandidate, error) {
	prepared, err := s.prepareCandidate(ctx, app, revision, p, opID, journal)
	if err != nil {
		return startedCandidate{Container: prepared.Container}, err
	}
	return s.startPrepared(ctx, app, p, prepared)
}
