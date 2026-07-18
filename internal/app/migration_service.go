package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// MigrationService is the sole Phase 2 orchestration facade.  It deliberately
// does not launch components, switch listeners, delete volumes, or access a
// runtime socket; those mutations are introduced by later migration phases.
type MigrationService struct {
	preflight *MigrationPreflight
	store     *MigrationCheckpointStore
	now       func() time.Time
}

func NewMigrationService(preflight *MigrationPreflight, store *MigrationCheckpointStore) (*MigrationService, error) {
	if preflight == nil || store == nil {
		return nil, fmt.Errorf("migration preflight and checkpoint store are required")
	}
	return &MigrationService{preflight: preflight, store: store, now: time.Now}, nil
}

func (s *MigrationService) Plan(ctx context.Context) (MigrationPreflightReport, error) {
	if s == nil || s.preflight == nil {
		return MigrationPreflightReport{}, fmt.Errorf("migration service is not configured")
	}
	return s.preflight.Check(ctx), nil
}

// Prepare records an idempotent retry point after all read-only preflight
// checks pass. Component launch is intentionally deferred to Phase 4.
func (s *MigrationService) Prepare(ctx context.Context, checkpoint MigrationCheckpoint) (*MigrationCheckpoint, error) {
	report, err := s.Plan(ctx)
	if err != nil {
		return nil, err
	}
	if !report.Ready {
		return nil, fmt.Errorf("migration preflight failed")
	}
	if checkpoint.MigrationID == "" {
		if existing, loadErr := s.store.Load(); loadErr == nil {
			checkpoint = *existing
		} else if errors.Is(loadErr, os.ErrNotExist) {
			checkpoint.MigrationID = "migration"
		} else {
			return nil, loadErr
		}
	}
	if checkpoint.Phase == "" || checkpoint.Phase == MigrationPhasePlanned {
		checkpoint.Phase = MigrationPhasePrepared
	}
	if checkpoint.Phase != MigrationPhasePrepared {
		return nil, fmt.Errorf("prepare requires prepared phase")
	}
	if checkpoint.StartedAt.IsZero() {
		checkpoint.StartedAt = s.now().UTC()
	}
	if err := s.store.Save(checkpoint); err != nil {
		return nil, err
	}
	return s.store.Load()
}

// Switch remains deliberately unavailable until traffic preconditions and the
// runtime-owned mutation channel exist. It cannot accidentally cut traffic.
func (s *MigrationService) Switch(context.Context) (*MigrationCheckpoint, error) {
	return nil, fmt.Errorf("safe traffic switch is not available: %w", ErrMigrationNotReady)
}

func (s *MigrationService) Status() (*MigrationCheckpoint, error) {
	checkpoint, err := s.store.Load()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return checkpoint, err
}

// Resume advances only through the same safe prepare checkpoint; a previous
// generation/phase can never regress because Store.Save enforces monotonicity.
func (s *MigrationService) Resume(ctx context.Context) (*MigrationCheckpoint, error) {
	checkpoint, err := s.Status()
	if err != nil {
		return nil, err
	}
	if checkpoint == nil {
		return nil, fmt.Errorf("no migration checkpoint: %w", ErrMigrationNotReady)
	}
	if checkpoint.Phase == MigrationPhaseSwitched {
		return checkpoint, nil
	}
	checkpoint.Phase = MigrationPhasePrepared
	return s.Prepare(ctx, *checkpoint)
}

var ErrMigrationNotReady = errors.New("migration operation is not ready")
