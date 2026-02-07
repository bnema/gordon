package domain

import "time"

// CronSchedule represents a recurring schedule.
//
// Preset schedules currently use fixed UTC times by design (MVP scope).
// Timezone-aware scheduling is deferred until a timezone field is added.
// When timezone support lands, host system timezone should be the default.
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
