package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

const backupTimestampLayout = "20060102T150405Z"

// BackupStorage implements backup artifact persistence on local filesystem.
type BackupStorage struct {
	rootDir string
	log     zerowrap.Logger
}

// NewBackupStorage creates a new filesystem backup storage.
func NewBackupStorage(rootDir string, log zerowrap.Logger) (*BackupStorage, error) {
	rootDir = expandTilde(rootDir)

	if err := os.MkdirAll(rootDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create backup directory: %w", err)
	}

	return &BackupStorage{rootDir: rootDir, log: log}, nil
}

// expandTilde replaces a leading "~/" with the user's home directory.
func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, path[2:])
	}
	return path
}

// Store saves backup data and returns the absolute storage path.
func (s *BackupStorage) Store(_ context.Context, domainName, dbName string, schedule domain.BackupSchedule, timestamp time.Time, data io.Reader) (string, error) {
	domainPart := sanitizeBackupPathComponent(domainName)
	dbPart := sanitizeBackupPathComponent(dbName)
	schedulePart := string(schedule)
	if schedulePart == "" {
		schedulePart = "manual"
	}
	schedulePart = sanitizeBackupPathComponent(schedulePart)

	backupDir := filepath.Join(s.rootDir, domainPart, dbPart, schedulePart)
	if err := os.MkdirAll(backupDir, 0750); err != nil {
		return "", fmt.Errorf("failed to create backup path: %w", err)
	}

	fileName := timestamp.UTC().Format(backupTimestampLayout) + ".bak"
	finalPath := filepath.Join(backupDir, fileName)
	tmpPath := finalPath + ".tmp"

	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("failed to create temp backup file: %w", err)
	}

	if _, err := io.Copy(f, data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to write backup data: %w", err)
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to close temp backup file: %w", err)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to finalize backup file: %w", err)
	}

	return finalPath, nil
}

// Get retrieves a backup file by path.
func (s *BackupStorage) Get(_ context.Context, path string) (io.ReadCloser, error) {
	if !pathWithinRoot(s.rootDir, path) {
		return nil, fmt.Errorf("backup path escapes storage root")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// List returns backups for a domain, optionally filtered by schedule.
func (s *BackupStorage) List(_ context.Context, domainName string, schedule *domain.BackupSchedule) ([]domain.BackupJob, error) {
	domainRoot := filepath.Join(s.rootDir, sanitizeBackupPathComponent(domainName))
	if _, err := os.Stat(domainRoot); err != nil {
		if os.IsNotExist(err) {
			return []domain.BackupJob{}, nil
		}
		return nil, err
	}

	jobs := make([]domain.BackupJob, 0)
	err := filepath.WalkDir(domainRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(domainRoot, path)
		if err != nil {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) != 3 {
			return nil
		}

		dbName := parts[0]
		sched := domain.BackupSchedule(parts[1])
		if schedule != nil && *schedule != sched {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		startedAt := info.ModTime().UTC()
		base := filepath.Base(path)
		if ts, err := time.Parse(backupTimestampLayout+".bak", base); err == nil {
			startedAt = ts.UTC()
		}

		jobs = append(jobs, domain.BackupJob{
			ID:        base,
			Domain:    domainName,
			DBName:    dbName,
			Schedule:  sched,
			Type:      domain.BackupTypeLogical,
			Status:    domain.BackupStatusCompleted,
			StartedAt: startedAt,
			SizeBytes: info.Size(),
			FilePath:  path,
		})

		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].StartedAt.After(jobs[j].StartedAt)
	})

	return jobs, nil
}

// Delete removes a backup file.
func (s *BackupStorage) Delete(_ context.Context, path string) error {
	if !pathWithinRoot(s.rootDir, path) {
		return fmt.Errorf("backup path escapes storage root")
	}
	return os.Remove(path)
}

// ApplyRetention removes old backups according to the schedule policy.
func (s *BackupStorage) ApplyRetention(ctx context.Context, domainName string, policy domain.RetentionPolicy) (int, error) {
	jobs, err := s.List(ctx, domainName, nil)
	if err != nil {
		return 0, err
	}

	groups := make(map[string][]domain.BackupJob)
	for _, job := range jobs {
		key := fmt.Sprintf("%s|%s", job.DBName, job.Schedule)
		groups[key] = append(groups[key], job)
	}

	deleted := 0
	for _, group := range groups {
		sort.Slice(group, func(i, j int) bool {
			return group[i].StartedAt.After(group[j].StartedAt)
		})

		keep := retentionKeepCount(policy, group[0].Schedule)
		if keep < 0 {
			continue
		}

		for idx := keep; idx < len(group); idx++ {
			if err := s.Delete(ctx, group[idx].FilePath); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return deleted, err
			}
			if err := s.removeEmptyBackupDirs(group[idx].FilePath, domainName); err != nil {
				return deleted, err
			}
			deleted++
		}
	}

	return deleted, nil
}

func (s *BackupStorage) removeEmptyBackupDirs(filePath, domainName string) error {
	backupRoot := s.rootDir
	domainRoot := filepath.Join(backupRoot, sanitizeBackupPathComponent(domainName))

	dir := filepath.Dir(filePath)
	for {
		if !pathWithinRoot(backupRoot, dir) {
			return nil
		}
		if dir == domainRoot || dir == backupRoot {
			break
		}

		err := os.Remove(dir)
		if err == nil {
			dir = filepath.Dir(dir)
			continue
		}
		if os.IsNotExist(err) {
			return nil
		}
		if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
			return nil
		}
		return err
	}

	return nil
}

func retentionKeepCount(policy domain.RetentionPolicy, schedule domain.BackupSchedule) int {
	switch schedule {
	case domain.ScheduleHourly:
		return policy.Hourly
	case domain.ScheduleDaily:
		return policy.Daily
	case domain.ScheduleWeekly:
		return policy.Weekly
	case domain.ScheduleMonthly:
		return policy.Monthly
	default:
		return -1
	}
}

func sanitizeBackupPathComponent(input string) string {
	clean := strings.TrimSpace(input)
	if clean == "" {
		return "unknown"
	}
	if strings.Trim(clean, ".") == "" {
		return "unknown"
	}
	clean = strings.Trim(clean, ".")
	if clean == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return replacer.Replace(clean)
}

func pathWithinRoot(root, path string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false
	}

	rootClean := filepath.Clean(rootAbs)
	pathClean := filepath.Clean(pathAbs)
	rel, err := filepath.Rel(rootClean, pathClean)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
