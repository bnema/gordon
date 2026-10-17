package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/bnema/gordon/internal/domain"
)

func newTestMetrics(t *testing.T) (*Metrics, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(prev) })
	m, err := NewMetrics()
	require.NoError(t, err)
	return m, reader
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Aggregation {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	out := map[string]metricdata.Aggregation{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m.Data
		}
	}
	return out
}

func attrValue(set attribute.Set, key string) string {
	v, _ := set.Value(attribute.Key(key))
	return v.AsString()
}

// gaugeByTarget indexes float gauge points by their target attribute.
func gaugeByTarget(t *testing.T, data metricdata.Aggregation) map[string]float64 {
	t.Helper()
	gauge, ok := data.(metricdata.Gauge[float64])
	require.True(t, ok, "expected a float64 gauge, got %T", data)
	out := map[string]float64{}
	for _, dp := range gauge.DataPoints {
		out[attrValue(dp.Attributes, "target")] = dp.Value
	}
	return out
}

func TestRecordBackupRun_CountsResultAndTracksLastSuccess(t *testing.T) {
	m, reader := newTestMetrics(t)
	ctx := context.Background()
	done := time.Date(2026, 10, 18, 1, 30, 0, 0, time.UTC)

	m.RecordBackupRun(ctx, domain.BackupRunRecord{
		Kind: domain.BackupKindVolume, App: "forgejo", Service: "forgejo", Target: "forgejo-data",
		Succeeded: true, Duration: 40 * time.Second, SizeBytes: 1024, FinishedAt: done,
	})
	m.RecordBackupRun(ctx, domain.BackupRunRecord{
		Kind: domain.BackupKindVolume, App: "forgejo", Service: "forgejo", Target: "forgejo-data",
		Succeeded: false, Duration: time.Second, FinishedAt: done.Add(time.Hour),
	})

	data := collect(t, reader)

	runs, ok := data["gordon.backup.runs"].(metricdata.Sum[int64])
	require.True(t, ok)
	results := map[string]int64{}
	for _, dp := range runs.DataPoints {
		assert.Equal(t, "volume", attrValue(dp.Attributes, "kind"))
		assert.Equal(t, "forgejo", attrValue(dp.Attributes, "app"))
		assert.Equal(t, "forgejo", attrValue(dp.Attributes, "service"))
		assert.Equal(t, "forgejo-data", attrValue(dp.Attributes, "target"))
		results[attrValue(dp.Attributes, "result")] = dp.Value
	}
	assert.Equal(t, map[string]int64{"success": 1, "failure": 1}, results)

	hist, ok := data["gordon.backup.duration_seconds"].(metricdata.Histogram[float64])
	require.True(t, ok)
	var count uint64
	for _, dp := range hist.DataPoints {
		count += dp.Count
	}
	assert.Equal(t, uint64(2), count)

	// A failure never moves the last-success timestamp or size.
	assert.Equal(t, map[string]float64{"forgejo-data": float64(done.Unix())},
		gaugeByTarget(t, data["gordon.backup.last_success_timestamp_seconds"]))
	assert.Equal(t, map[string]float64{"forgejo-data": 1024},
		gaugeByTarget(t, data["gordon.backup.last_size_bytes"]))
}

func TestObserveStoredBackup_KeepsTheNewestSuccess(t *testing.T) {
	m, reader := newTestMetrics(t)
	newer := time.Date(2026, 10, 17, 1, 30, 0, 0, time.UTC)
	older := newer.Add(-24 * time.Hour)
	rec := domain.BackupRunRecord{Kind: domain.BackupKindDatabase, App: "shop", Service: "db", Target: "orders", Succeeded: true}

	rec.FinishedAt, rec.SizeBytes = newer, 200
	m.ObserveStoredBackup(rec)
	rec.FinishedAt, rec.SizeBytes = older, 100
	m.ObserveStoredBackup(rec)

	data := collect(t, reader)
	assert.Equal(t, map[string]float64{"orders": float64(newer.Unix())},
		gaugeByTarget(t, data["gordon.backup.last_success_timestamp_seconds"]))
	assert.Equal(t, map[string]float64{"orders": 200},
		gaugeByTarget(t, data["gordon.backup.last_size_bytes"]))

	// Seeding never counts a run.
	_, counted := data["gordon.backup.runs"]
	assert.False(t, counted)
}

func TestObserveBackupNextRun(t *testing.T) {
	m, reader := newTestMetrics(t)
	next := time.Date(2026, 10, 18, 1, 30, 0, 0, time.UTC)
	m.ObserveBackupNextRun(domain.BackupKindVolume, func() time.Time { return next })
	m.ObserveBackupNextRun(domain.BackupKindDatabase, func() time.Time { return time.Time{} })

	gauge, ok := collect(t, reader)["gordon.backup.next_run_timestamp_seconds"].(metricdata.Gauge[float64])
	require.True(t, ok)
	require.Len(t, gauge.DataPoints, 1, "an unknown next run is not exported")
	assert.Equal(t, "volume", attrValue(gauge.DataPoints[0].Attributes, "kind"))
	assert.Equal(t, float64(next.Unix()), gauge.DataPoints[0].Value)
}
