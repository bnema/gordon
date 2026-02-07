package cron

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/zerowrap"
)

// Scheduler runs recurring jobs based on backup schedules.
type Scheduler struct {
	entries map[string]*entry
	mu      sync.RWMutex
	stopCh  chan struct{}
	log     zerowrap.Logger
	nowFn   func() time.Time
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
func NewScheduler(log zerowrap.Logger) *Scheduler {
	return &Scheduler{
		entries: make(map[string]*entry),
		stopCh:  make(chan struct{}),
		log:     log,
		nowFn: func() time.Time {
			return time.Now().UTC()
		},
	}
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
func (s *Scheduler) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, id)
}

// Start begins the scheduler loop.
func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	go func() {
		defer ticker.Stop()
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

// Stop stops the scheduler loop.
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

// RunNow triggers a registered job immediately.
func (s *Scheduler) RunNow(id string) error {
	e := s.getEntry(id)
	if e == nil {
		return fmt.Errorf("schedule %q not found", id)
	}

	return s.executeEntry(context.Background(), e)
}

func (s *Scheduler) runDue(ctx context.Context) {
	now := s.nowFn()
	entries := s.snapshotEntries()
	for _, e := range entries {
		if now.Before(e.nextRun) {
			continue
		}

		e := e
		go func() {
			if err := s.executeEntry(ctx, e); err != nil {
				s.log.Warn().Err(err).Str("schedule_id", e.id).Msg("scheduled job failed")
			}
		}()
	}
}

func (s *Scheduler) executeEntry(ctx context.Context, e *entry) error {
	if !e.running.CompareAndSwap(false, true) {
		return fmt.Errorf("schedule %q is already running", e.id)
	}
	defer e.running.Store(false)

	now := s.nowFn()
	err := e.job(ctx)

	nextRun, nextErr := calculateNextRun(now, e.schedule)
	if nextErr != nil {
		return nextErr
	}

	s.mu.Lock()
	e.lastRun = now
	e.nextRun = nextRun
	s.mu.Unlock()

	return err
}

func (s *Scheduler) getEntry(id string) *entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.entries[id]
}

func (s *Scheduler) snapshotEntries() []*entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make([]*entry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, e)
	}
	return entries
}

func calculateNextRun(now time.Time, schedule domain.CronSchedule) (time.Time, error) {
	now = now.UTC()

	switch schedule.Preset {
	case domain.ScheduleHourly:
		next := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, time.UTC)
		if !next.After(now) {
			next = next.Add(time.Hour)
		}
		return next, nil
	case domain.ScheduleDaily:
		next := time.Date(now.Year(), now.Month(), now.Day(), 2, 0, 0, 0, time.UTC)
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		return next, nil
	case domain.ScheduleWeekly:
		daysUntilSunday := (7 - int(now.Weekday())) % 7
		next := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, time.UTC).AddDate(0, 0, daysUntilSunday)
		if !next.After(now) {
			next = next.AddDate(0, 0, 7)
		}
		return next, nil
	case domain.ScheduleMonthly:
		next := time.Date(now.Year(), now.Month(), 1, 4, 0, 0, 0, time.UTC)
		if !next.After(now) {
			next = next.AddDate(0, 1, 0)
		}
		return next, nil
	default:
		return time.Time{}, fmt.Errorf("unsupported schedule preset: %q", schedule.Preset)
	}
}
