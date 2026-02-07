package in

import (
	"context"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// BackupService defines backup orchestration use cases.
type BackupService interface {
	ListBackups(ctx context.Context, domainName string) ([]domain.BackupJob, error)
	RunBackup(ctx context.Context, domainName, dbName string) (*domain.BackupResult, error)
	Restore(ctx context.Context, domainName, backupID string) error
	RestorePITR(ctx context.Context, domainName string, targetTime time.Time) error
	Status(ctx context.Context) ([]domain.BackupJob, error)
	DetectDatabases(ctx context.Context, domainName string) ([]domain.DBInfo, error)
}
