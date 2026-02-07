package out

import (
	"context"
	"io"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// BackupStorage defines persistence for backup artifacts and metadata.
type BackupStorage interface {
	Store(ctx context.Context, domainName, dbName string, schedule domain.BackupSchedule, timestamp time.Time, data io.Reader) (string, error)
	Get(ctx context.Context, path string) (io.ReadCloser, error)
	List(ctx context.Context, domainName string, schedule *domain.BackupSchedule) ([]domain.BackupJob, error)
	Delete(ctx context.Context, path string) error
	ApplyRetention(ctx context.Context, domainName string, policy domain.RetentionPolicy) (int, error)
}
