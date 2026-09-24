package telemetry

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func collectManaged(t *testing.T, reader *sdkmetric.ManualReader) (int64, bool) {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "gordon.container.managed" {
				continue
			}
			gauge, ok := m.Data.(metricdata.Gauge[int64])
			require.True(t, ok)
			require.Len(t, gauge.DataPoints, 1)
			assert.Equal(t, 0, gauge.DataPoints[0].Attributes.Len(), "gauge must stay label-free")
			return gauge.DataPoints[0].Value, true
		}
	}
	return 0, false
}

func TestObserveManagedContainers(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	m, err := NewMetrics()
	require.NoError(t, err)

	current := int64(3)
	var countErr error
	require.NoError(t, m.ObserveManagedContainers(func(context.Context) (int64, error) {
		return current, countErr
	}))

	v, ok := collectManaged(t, reader)
	require.True(t, ok)
	assert.Equal(t, int64(3), v)

	current = 1
	v, ok = collectManaged(t, reader)
	require.True(t, ok)
	assert.Equal(t, int64(1), v)

	// A failed count skips the observation instead of exporting a wrong value.
	countErr = errors.New("state unavailable")
	var rm metricdata.ResourceMetrics
	_ = reader.Collect(context.Background(), &rm)
	for _, sm := range rm.ScopeMetrics {
		for _, metric := range sm.Metrics {
			assert.NotEqual(t, "gordon.container.managed", metric.Name)
		}
	}
}
