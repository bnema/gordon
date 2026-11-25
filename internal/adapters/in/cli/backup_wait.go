package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// backupPollInterval is how often --wait asks the server for a run's state.
var backupPollInterval = 2 * time.Second

// backupRunSettled reports whether a run status is final.
func backupRunSettled(status string) bool {
	return status == string(domain.BackupStatusCompleted) || status == string(domain.BackupStatusFailed)
}

// backupRunFailed reports whether a run ended in failure.
func backupRunFailed(status string) bool {
	return status == string(domain.BackupStatusFailed)
}

// waitForBackupRun polls fetch until the run's status is final. The server
// keeps running the backup when waiting stops, so a cancelled wait says so.
func waitForBackupRun[J any](ctx context.Context, job J, status func(J) string, fetch func(context.Context) (J, error)) (J, error) {
	ticker := time.NewTicker(backupPollInterval)
	defer ticker.Stop()
	for !backupRunSettled(status(job)) {
		select {
		case <-ctx.Done():
			return job, fmt.Errorf("stopped waiting, the backup keeps running on the server: %w", ctx.Err())
		case <-ticker.C:
		}
		next, err := fetch(ctx)
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return job, fmt.Errorf("stopped waiting, the backup keeps running on the server: %w", ctx.Err())
			}
			return job, fmt.Errorf("failed to poll backup run: %w", err)
		}
		job = next
	}
	return job, nil
}
