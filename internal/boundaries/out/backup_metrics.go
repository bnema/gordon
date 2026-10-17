package out

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// BackupMetrics records backup outcomes.
// Implementations must be safe for concurrent use.
type BackupMetrics interface {
	// RecordBackupRun counts one finished backup run. A successful run
	// also becomes the target's last success.
	RecordBackupRun(ctx context.Context, run domain.BackupRunRecord)
	// ObserveStoredBackup reports an existing stored backup as a success
	// without counting a run, so the last success survives a restart.
	// Older records never replace a newer one.
	ObserveStoredBackup(run domain.BackupRunRecord)
	// RetainBackupTargets forgets the last success of every target of kind
	// that is not in targets, so a removed app or backup declaration stops
	// being reported.
	RetainBackupTargets(kind domain.BackupKind, targets []domain.BackupTargetRef)
}
