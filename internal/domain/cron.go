package domain

import "time"

// CronSchedule represents a recurring schedule.
type CronSchedule struct {
	Preset BackupSchedule
}

// CronEntry represents a registered cron job.
type CronEntry struct {
	ID       string
	Name     string
	Schedule CronSchedule
	LastRun  time.Time
	NextRun  time.Time
	Running  bool
}
