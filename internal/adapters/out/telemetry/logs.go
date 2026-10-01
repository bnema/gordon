package telemetry

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// Attribute keys shared by every exported record.
const (
	attrGordonApp     = "gordon.app"
	attrGordonService = "gordon.service"
	attrLogType       = "log.type"
	attrLogStream     = "log.iostream"
)

var _ out.LogExporter = (*LogExporter)(nil)

// LogExporter implements out.LogExporter over OTLP.
//
// OTLP carries service identity on the resource, not on the record, so
// each source (Gordon or one app service) gets its own lazily created
// LoggerProvider and resource. All providers share one exporter; the
// batch processors of every provider serialize through it.
type LogExporter struct {
	exporter sdklog.Exporter
	shared   *serialExporter
	base     *resource.Resource

	mu        sync.Mutex
	providers map[domain.LogSource]*sdklog.LoggerProvider
	closed    bool
}

// NewLogExporter wraps exporter. base carries host/OS/version attributes
// merged into every source resource; it may be nil.
func NewLogExporter(exporter sdklog.Exporter, base *resource.Resource) *LogExporter {
	shared := &serialExporter{Exporter: exporter}
	return &LogExporter{
		exporter:  exporter,
		shared:    shared,
		base:      base,
		providers: map[domain.LogSource]*sdklog.LoggerProvider{},
	}
}

// Export implements out.LogExporter. It never blocks on the network:
// the batch processor queues records and drops the oldest on overload.
func (e *LogExporter) Export(ctx context.Context, record domain.LogRecord) {
	provider := e.provider(record.Source)
	if provider == nil {
		return
	}
	var r otellog.Record
	r.SetTimestamp(record.Time)
	r.SetObservedTimestamp(record.Time)
	r.SetBody(attribute.StringValue(record.Body))
	if sev, text := severity(record.Severity); sev != otellog.SeverityUndefined {
		r.SetSeverity(sev)
		r.SetSeverityText(text)
	}
	attrs := make([]attribute.KeyValue, 0, len(record.Attributes)+2)
	if record.Type != "" {
		attrs = append(attrs, attribute.String(attrLogType, record.Type))
	}
	if record.Stream != "" {
		attrs = append(attrs, attribute.String(attrLogStream, record.Stream))
	}
	for key, value := range record.Attributes {
		attrs = append(attrs, attribute.String(key, value))
	}
	r.AddAttributes(attrs...)
	provider.Logger("gordon").Emit(ctx, r)
}

// Shutdown flushes every source provider, then closes the exporter.
func (e *LogExporter) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.closed = true
	providers := e.providers
	e.providers = map[domain.LogSource]*sdklog.LoggerProvider{}
	e.mu.Unlock()

	var firstErr error
	for _, provider := range providers {
		if err := provider.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := e.exporter.Shutdown(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (e *LogExporter) provider(source domain.LogSource) *sdklog.LoggerProvider {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	if provider, ok := e.providers[source]; ok {
		return provider
	}
	// sourceResource is schemaless, so Merge cannot fail on schema
	// conflicts; its attributes win over base (service.name).
	res, err := resource.Merge(e.base, sourceResource(source))
	if err != nil {
		res = sourceResource(source)
	}
	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(e.shared)),
		sdklog.WithResource(res),
	)
	e.providers[source] = provider
	return provider
}

// sourceResource builds the resource identity of one source:
// service.name "<app>.<service>" plus namespace and Gordon attributes.
func sourceResource(source domain.LogSource) *resource.Resource {
	attrs := []attribute.KeyValue{semconv.ServiceName(source.ServiceName())}
	if !source.IsGordon() {
		attrs = append(attrs,
			semconv.ServiceNamespace(source.App),
			attribute.String(attrGordonApp, source.App),
			attribute.String(attrGordonService, source.Service),
		)
	}
	return resource.NewSchemaless(attrs...)
}

func severity(level domain.LogSeverity) (otellog.Severity, string) {
	switch level {
	case domain.LogSeverityInfo:
		return otellog.SeverityInfo, "INFO"
	case domain.LogSeverityWarn:
		return otellog.SeverityWarn, "WARN"
	case domain.LogSeverityError:
		return otellog.SeverityError, "ERROR"
	default:
		return otellog.SeverityUndefined, ""
	}
}

// serialExporter serializes Export calls: the SDK forbids concurrent
// Export on one exporter, and each source provider runs its own batcher.
// Shutdown is ignored so one source provider shutting down cannot close
// the exporter shared by the others; the owner shuts it down once.
type serialExporter struct {
	sdklog.Exporter
	mu sync.Mutex
}

func (s *serialExporter) Export(ctx context.Context, records []sdklog.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Exporter.Export(ctx, records)
}

func (s *serialExporter) Shutdown(context.Context) error { return nil }
