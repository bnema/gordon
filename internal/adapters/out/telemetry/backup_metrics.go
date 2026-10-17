package telemetry

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

var _ out.BackupMetrics = (*Metrics)(nil)

// backupTargetKey identifies one backed-up resource.
type backupTargetKey struct {
	kind    domain.BackupKind
	app     string
	service string
	target  string
}

func (k backupTargetKey) attrs() []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("kind", string(k.kind)),
		attribute.String("app", k.app),
		attribute.String("service", k.service),
		attribute.String("target", k.target),
	}
}

type backupLastSuccess struct {
	at   time.Time
	size int64
}

// backupState holds the last success per target, exported by observable
// gauges at each collection.
type backupState struct {
	mu      sync.Mutex
	last    map[backupTargetKey]backupLastSuccess
	nextRun map[domain.BackupKind]func() time.Time
}

func newBackupState() *backupState {
	return &backupState{
		last:    map[backupTargetKey]backupLastSuccess{},
		nextRun: map[domain.BackupKind]func() time.Time{},
	}
}

func keyOf(run domain.BackupRunRecord) backupTargetKey {
	return backupTargetKey{kind: run.Kind, app: run.App, service: run.Service, target: run.Target}
}

// remember stores a success unless a newer one is already known.
func (s *backupState) remember(run domain.BackupRunRecord) {
	if !run.Succeeded || run.FinishedAt.IsZero() {
		return
	}
	key := keyOf(run)
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.last[key]; ok && !run.FinishedAt.After(prev.at) {
		return
	}
	s.last[key] = backupLastSuccess{at: run.FinishedAt, size: run.SizeBytes}
}

// registerBackupInstruments creates the backup counters, histogram, and
// observable gauges.
func (m *Metrics) registerBackupInstruments() error {
	var err error
	if m.BackupRuns, err = m.meter.Int64Counter("gordon.backup.runs",
		metric.WithDescription("Finished backup runs by result")); err != nil {
		return err
	}
	if m.BackupDuration, err = m.meter.Float64Histogram("gordon.backup.duration_seconds",
		metric.WithDescription("Backup run duration in seconds"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(1, 5, 15, 30, 60, 120, 300, 600, 1800, 3600)); err != nil {
		return err
	}
	lastSuccess, err := m.meter.Float64ObservableGauge("gordon.backup.last_success_timestamp_seconds",
		metric.WithDescription("Unix time of the last successful backup per target"),
		metric.WithUnit("s"))
	if err != nil {
		return err
	}
	lastSize, err := m.meter.Float64ObservableGauge("gordon.backup.last_size_bytes",
		metric.WithDescription("Size of the last successful backup per target"),
		metric.WithUnit("By"))
	if err != nil {
		return err
	}
	nextRun, err := m.meter.Float64ObservableGauge("gordon.backup.next_run_timestamp_seconds",
		metric.WithDescription("Unix time of the next scheduled backup run"),
		metric.WithUnit("s"))
	if err != nil {
		return err
	}
	_, err = m.meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		m.backups.mu.Lock()
		defer m.backups.mu.Unlock()
		for key, last := range m.backups.last {
			attrs := metric.WithAttributes(key.attrs()...)
			o.ObserveFloat64(lastSuccess, float64(last.at.Unix()), attrs)
			o.ObserveFloat64(lastSize, float64(last.size), attrs)
		}
		for kind, next := range m.backups.nextRun {
			if at := next(); !at.IsZero() {
				o.ObserveFloat64(nextRun, float64(at.Unix()), metric.WithAttributes(attribute.String("kind", string(kind))))
			}
		}
		return nil
	}, lastSuccess, lastSize, nextRun)
	return err
}

// RecordBackupRun implements out.BackupMetrics.
func (m *Metrics) RecordBackupRun(ctx context.Context, run domain.BackupRunRecord) {
	result := "failure"
	if run.Succeeded {
		result = "success"
	}
	attrs := keyOf(run).attrs()
	m.BackupRuns.Add(ctx, 1, metric.WithAttributes(append(attrs, attribute.String("result", result))...))
	m.BackupDuration.Record(ctx, run.Duration.Seconds(), metric.WithAttributes(attrs...))
	m.backups.remember(run)
}

// ObserveStoredBackup implements out.BackupMetrics.
func (m *Metrics) ObserveStoredBackup(run domain.BackupRunRecord) {
	m.backups.remember(run)
}

// ObserveBackupNextRun exports the next planned run of one backup kind.
// next is called at each collection; a zero time is not exported.
func (m *Metrics) ObserveBackupNextRun(kind domain.BackupKind, next func() time.Time) {
	m.backups.mu.Lock()
	defer m.backups.mu.Unlock()
	m.backups.nextRun[kind] = next
}
