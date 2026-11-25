package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/domain"
)

const (
	// backupPollInterval is how often --wait asks the server for a run's state.
	backupPollInterval = 2 * time.Second
	// backupPollAttempts is how many polls in a row may fail before --wait
	// gives up, so a short network drop does not end the wait.
	backupPollAttempts = 3
)

// backupRunSettled reports whether a run status is final.
func backupRunSettled(status string) bool {
	return status == string(domain.BackupStatusCompleted) || status == string(domain.BackupStatusFailed)
}

// backupRunFailed reports whether a run ended in failure.
func backupRunFailed(status string) bool {
	return status == string(domain.BackupStatusFailed)
}

// waitForBackupRun polls fetch until the run's status is final. The server
// keeps running the backup when waiting stops, so every early return says
// so and names the run.
func waitForBackupRun[J any](ctx context.Context, id string, job J, status func(J) string, fetch func(context.Context) (J, error)) (J, error) {
	ticker := time.NewTicker(backupPollInterval)
	defer ticker.Stop()
	failures := 0
	for !backupRunSettled(status(job)) {
		select {
		case <-ctx.Done():
			return job, fmt.Errorf("stopped waiting for backup run %s, it keeps running on the server: %w", id, ctx.Err())
		case <-ticker.C:
		}
		next, err := fetch(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return job, fmt.Errorf("stopped waiting for backup run %s, it keeps running on the server: %w", id, ctx.Err())
			}
			if isNotFound(err) {
				return job, fmt.Errorf("backup run %s is no longer tracked by the server (it may have restarted); check the status command: %w", id, err)
			}
			failures++
			if failures < backupPollAttempts {
				continue
			}
			return job, fmt.Errorf("failed to poll backup run %s, it may still be running on the server: %w", id, err)
		}
		failures = 0
		job = next
	}
	return job, nil
}

// isNotFound reports whether err is an HTTP 404 from the server.
func isNotFound(err error) bool {
	var httpErr *remote.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound
}
