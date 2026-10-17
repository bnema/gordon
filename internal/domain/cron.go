package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronSchedule represents a recurring schedule: a preset, an optional time
// of day, and the timezone that time is read in.
//
// When At is nil the preset keeps its built-in time (daily 02:00, weekly
// Sunday 03:00, monthly day 1 04:00, hourly on the hour). When Location is
// nil times are read in UTC.
type CronSchedule struct {
	Preset   BackupSchedule
	At       *TimeOfDay
	Location *time.Location
}

// TimeOfDay is a wall-clock time. Hourly schedules use only Minute.
type TimeOfDay struct {
	Hour   int
	Minute int
}

// String formats the time as HH:MM.
func (t TimeOfDay) String() string {
	return fmt.Sprintf("%02d:%02d", t.Hour, t.Minute)
}

// ParseTimeOfDay parses a 24-hour "HH:MM" wall-clock time.
func ParseTimeOfDay(raw string) (TimeOfDay, error) {
	hh, mm, ok := strings.Cut(strings.TrimSpace(raw), ":")
	if !ok || len(hh) == 0 || len(hh) > 2 || len(mm) != 2 {
		return TimeOfDay{}, fmt.Errorf("time must use 24-hour HH:MM format, got %q", raw)
	}
	hour, errH := strconv.Atoi(hh)
	minute, errM := strconv.Atoi(mm)
	if errH != nil || errM != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return TimeOfDay{}, fmt.Errorf("time must use 24-hour HH:MM format, got %q", raw)
	}
	return TimeOfDay{Hour: hour, Minute: minute}, nil
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
