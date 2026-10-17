package cron

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/zerowrap"
)

// Scheduler runs recurring jobs on preset schedules. Times are wall-clock
// times in each schedule's Location; nowFn returns an absolute instant.
type Scheduler struct {
	entries map[string]*entry
	mu      sync.RWMutex
	stopCh  chan struct{}
	log     zerowrap.Logger
	nowFn   func() time.Time
	maxJobs int
	started atomic.Bool
}

// Option configures a Scheduler.
type Option func(*Scheduler)

var ErrJobRunning = errors.New("schedule is currently running")

// WithMaxJobs sets the maximum number of jobs run concurrently.
func WithMaxJobs(n int) Option {
	return func(s *Scheduler) {
		if n > 0 {
			s.maxJobs = n
		}
	}
}

type entry struct {
	id       string
	name     string
	schedule domain.CronSchedule
	job      func(ctx context.Context) error
	lastRun  time.Time
	nextRun  time.Time
	running  atomic.Bool
}

// NewScheduler creates a scheduler instance.
func NewScheduler(log zerowrap.Logger, opts ...Option) *Scheduler {
	s := &Scheduler{
		entries: make(map[string]*entry),
		stopCh:  make(chan struct{}),
		log:     log,
		maxJobs: 4,
		nowFn: func() time.Time {
			return time.Now().UTC()
		},
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	return s
}

// Add registers a new scheduled job.
func (s *Scheduler) Add(id, name string, sched domain.CronSchedule, job func(ctx context.Context) error) error {
	if id == "" {
		return fmt.Errorf("id is required")
	}
	if job == nil {
		return fmt.Errorf("job is required")
	}

	nextRun, err := calculateNextRun(s.nowFn(), sched)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.entries[id]; exists {
		return fmt.Errorf("schedule %q already exists", id)
	}

	s.entries[id] = &entry{
		id:       id,
		name:     name,
		schedule: sched,
		job:      job,
		nextRun:  nextRun,
	}

	return nil
}

// Remove unregisters a scheduled job.
func (s *Scheduler) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[id]
	if e == nil {
		return nil
	}
	if e.running.Load() {
		return fmt.Errorf("%w: %s", ErrJobRunning, id)
	}
	delete(s.entries, id)
	return nil
}

// Start begins the scheduler loop.
func (s *Scheduler) Start(ctx context.Context) {
	if !s.started.CompareAndSwap(false, true) {
		return
	}

	select {
	case <-s.stopCh:
		s.started.Store(false)
		return
	default:
	}

	select {
	case <-ctx.Done():
		s.started.Store(false)
		return
	default:
	}

	ticker := time.NewTicker(time.Minute)
	go func() {
		defer ticker.Stop()
		s.runDue(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stopCh:
				return
			case <-ticker.C:
				s.runDue(ctx)
			}
		}
	}()
}

// Stop stops Scheduler processing.
//
// Stop is idempotent and safe to call multiple times. Because Stop closes
// Scheduler.stopCh, the scheduler instance is not restartable after Stop;
// create a new Scheduler to start processing again.
func (s *Scheduler) Stop() {
	select {
	case <-s.stopCh:
		return
	default:
		close(s.stopCh)
	}
}

// List returns current scheduler entries.
func (s *Scheduler) List() []domain.CronEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make([]domain.CronEntry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, domain.CronEntry{
			ID:       e.id,
			Name:     e.name,
			Schedule: e.schedule,
			LastRun:  e.lastRun,
			NextRun:  e.nextRun,
			Running:  e.running.Load(),
		})
	}

	return entries
}

// RunNow triggers a registered job immediately with the provided context.
func (s *Scheduler) RunNow(ctx context.Context, id string) error {
	e := s.getEntry(id)
	if e == nil {
		return fmt.Errorf("schedule %q not found", id)
	}

	return s.executeEntry(ctx, e)
}

func (s *Scheduler) runDue(ctx context.Context) {
	now := s.nowFn()
	entries := s.dueEntries(now)
	sem := make(chan struct{}, s.maxJobs)
	var wg sync.WaitGroup
	for _, e := range entries {
		wg.Add(1)
		sem <- struct{}{}

		e := e
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.executeEntry(ctx, e); err != nil {
				s.log.Warn().Err(err).Str("schedule_id", e.id).Msg("scheduled job failed")
			}
		}()
	}
	wg.Wait()
}

func (s *Scheduler) executeEntry(ctx context.Context, e *entry) error {
	if !e.running.CompareAndSwap(false, true) {
		return fmt.Errorf("schedule %q is already running", e.id)
	}
	defer e.running.Store(false)

	now := s.nowFn()
	var jobErr error
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				jobErr = fmt.Errorf("job panic: %v", rec)
			}
		}()
		jobErr = e.job(ctx)
	}()

	completedAt := s.nowFn()
	if completedAt.Before(now) {
		completedAt = now
	}

	nextRun, nextErr := calculateNextRun(completedAt, e.schedule)
	if nextErr != nil {
		if jobErr != nil {
			return fmt.Errorf("job failed: %w; also failed to calculate next run: %v", jobErr, nextErr)
		}
		return nextErr
	}

	s.mu.Lock()
	e.lastRun = now
	e.nextRun = nextRun
	s.mu.Unlock()

	return jobErr
}

func (s *Scheduler) getEntry(id string) *entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.entries[id]
}

func (s *Scheduler) dueEntries(now time.Time) []*entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make([]*entry, 0, len(s.entries))
	for _, e := range s.entries {
		if now.Before(e.nextRun) {
			continue
		}
		entries = append(entries, e)
	}
	return entries
}

// presetDefaultTimes are the built-in times of day used when a schedule
// sets no explicit time.
var presetDefaultTimes = map[domain.BackupSchedule]domain.TimeOfDay{
	domain.ScheduleHourly:  {Hour: 0, Minute: 0},
	domain.ScheduleDaily:   {Hour: 2, Minute: 0},
	domain.ScheduleWeekly:  {Hour: 3, Minute: 0},
	domain.ScheduleMonthly: {Hour: 4, Minute: 0},
}

// calculateNextRun returns the first scheduled instant strictly after now.
//
// Times are wall-clock times in schedule.Location (UTC when nil), so a
// daily 03:30 stays 03:30 across daylight-saving changes. A wall time that
// does not exist on a given day (spring-forward gap) runs at the end of the
// gap. A wall time that happens twice (fall-back) runs once, at its first
// occurrence; hourly schedules follow real elapsed hours.
//
// The result only ever depends on now: a run missed while the process was
// down is skipped, never caught up.
//   - hourly:  every hour at At.Minute
//   - daily:   every day at At
//   - weekly:  every Sunday at At
//   - monthly: the first day of every month at At
func calculateNextRun(now time.Time, schedule domain.CronSchedule) (time.Time, error) {
	at, ok := presetDefaultTimes[schedule.Preset]
	if !ok {
		return time.Time{}, fmt.Errorf("unsupported schedule preset: %q", schedule.Preset)
	}
	if schedule.At != nil {
		at = *schedule.At
	}
	loc := schedule.Location
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	y, m, d := local.Date()

	switch schedule.Preset {
	case domain.ScheduleHourly:
		return nextMinuteOfHour(now, at.Minute, loc), nil
	case domain.ScheduleDaily:
		return firstAfter(now, func(i int) time.Time { return wallTime(y, m, d+i, at, loc) }), nil
	case domain.ScheduleWeekly:
		sunday := d + (7-int(local.Weekday()))%7
		return firstAfter(now, func(i int) time.Time { return wallTime(y, m, sunday+7*i, at, loc) }), nil
	default: // domain.ScheduleMonthly
		return firstAfter(now, func(i int) time.Time { return wallTime(y, m+time.Month(i), 1, at, loc) }), nil
	}
}

// firstAfter returns candidate(0) or candidate(1), whichever is the first
// strictly after now.
func firstAfter(now time.Time, candidate func(i int) time.Time) time.Time {
	if next := candidate(0); next.After(now) {
		return next
	}
	return candidate(1)
}

// nextMinuteOfHour returns the first whole minute strictly after now whose
// wall-clock minute in loc equals minute. Walking real minutes keeps hourly
// runs one hour apart across DST changes.
func nextMinuteOfHour(now time.Time, minute int, loc *time.Location) time.Time {
	t := now.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 120 && t.In(loc).Minute() != minute; i++ {
		t = t.Add(time.Minute)
	}
	return t
}

// wallTime returns the instant of the given wall-clock time in loc. Day and
// month overflow are normalized like time.Date. When the wall time falls in
// a spring-forward gap, the end of the gap is returned.
func wallTime(year int, month time.Month, day int, at domain.TimeOfDay, loc *time.Location) time.Time {
	t := time.Date(year, month, day, at.Hour, at.Minute, 0, 0, loc)
	if t.Hour() == at.Hour && t.Minute() == at.Minute {
		return t
	}
	// Nonexistent wall time: interpret it with the offset in force before
	// the gap, which lands after the transition, then snap back to it.
	naive := time.Date(year, month, day, at.Hour, at.Minute, 0, 0, time.UTC)
	_, offsetBefore := naive.Add(-12 * time.Hour).In(loc).Zone()
	start, _ := naive.Add(-time.Duration(offsetBefore) * time.Second).In(loc).ZoneBounds()
	return start.In(loc)
}
