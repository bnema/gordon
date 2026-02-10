package telemetry

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// Metrics holds Gordon-specific OTel metrics instruments.
type Metrics struct {
	// Deployments
	DeployTotal    metric.Int64Counter
	DeployDuration metric.Float64Histogram
	DeployErrors   metric.Int64Counter

	// Container lifecycle
	ContainerRestarts   metric.Int64Counter
	ContainerCrashLoops metric.Int64Counter
	ManagedContainers   metric.Int64UpDownCounter

	// Registry
	ImagePushTotal metric.Int64Counter
	ImagePushSize  metric.Int64Counter // bytes

	// Events
	EventsProcessed metric.Int64Counter
	EventsDropped   metric.Int64Counter
}

// NewMetrics creates and registers all Gordon metric instruments.
// Returns a noop-safe Metrics struct — all fields are always initialized
// (OTel returns noop instruments when no MeterProvider is set).
func NewMetrics() (*Metrics, error) {
	meter := otel.Meter("gordon")
	m := &Metrics{}
	var err error

	if m.DeployTotal, err = meter.Int64Counter("gordon.deploy.total",
		metric.WithDescription("Total number of deployments")); err != nil {
		return nil, err
	}
	if m.DeployDuration, err = meter.Float64Histogram("gordon.deploy.duration_seconds",
		metric.WithDescription("Deploy duration in seconds"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(1, 5, 10, 30, 60, 120, 300)); err != nil {
		return nil, err
	}
	if m.DeployErrors, err = meter.Int64Counter("gordon.deploy.errors",
		metric.WithDescription("Total deploy errors")); err != nil {
		return nil, err
	}
	if m.ContainerRestarts, err = meter.Int64Counter("gordon.container.restarts",
		metric.WithDescription("Total container restarts")); err != nil {
		return nil, err
	}
	if m.ContainerCrashLoops, err = meter.Int64Counter("gordon.container.crash_loops",
		metric.WithDescription("Total crash loop detections")); err != nil {
		return nil, err
	}
	if m.ManagedContainers, err = meter.Int64UpDownCounter("gordon.container.managed",
		metric.WithDescription("Currently managed containers")); err != nil {
		return nil, err
	}
	if m.ImagePushTotal, err = meter.Int64Counter("gordon.registry.push.total",
		metric.WithDescription("Total image pushes")); err != nil {
		return nil, err
	}
	if m.ImagePushSize, err = meter.Int64Counter("gordon.registry.push.bytes",
		metric.WithDescription("Total bytes pushed to registry"),
		metric.WithUnit("By")); err != nil {
		return nil, err
	}
	if m.EventsProcessed, err = meter.Int64Counter("gordon.events.processed",
		metric.WithDescription("Total events processed")); err != nil {
		return nil, err
	}
	if m.EventsDropped, err = meter.Int64Counter("gordon.events.dropped",
		metric.WithDescription("Total events dropped")); err != nil {
		return nil, err
	}

	return m, nil
}
