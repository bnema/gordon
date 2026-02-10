// Package telemetry provides OpenTelemetry initialization for Gordon.
// It configures trace, metric, and log providers that export via OTLP/HTTP.
package telemetry

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otellog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Config holds telemetry configuration.
type Config struct {
	Enabled         bool    `mapstructure:"enabled"`
	Endpoint        string  `mapstructure:"endpoint"`          // OTLP HTTP endpoint, e.g. "http://localhost:4318"
	AuthToken       string  `mapstructure:"auth_token"`        // Basic auth token (base64 encoded user:pass)
	Traces          bool    `mapstructure:"traces"`            // Enable trace export
	Metrics         bool    `mapstructure:"metrics"`           // Enable metric export
	Logs            bool    `mapstructure:"logs"`              // Bridge zerowrap logs to OTLP
	TraceSampleRate float64 `mapstructure:"trace_sample_rate"` // 0.0-1.0, default 1.0
}

// Provider holds the initialized OTel providers.
type Provider struct {
	TracerProvider *trace.TracerProvider
	MeterProvider  *metric.MeterProvider
	LogProvider    *otellog.LoggerProvider
}

// endpointConfig holds parsed endpoint details shared across exporter setup.
type endpointConfig struct {
	host     string
	basePath string
	insecure bool
	headers  map[string]string
}

// NewProvider creates and configures OTel providers based on the given config.
// Returns a noop provider if telemetry is disabled.
// The returned shutdown function must be called on application exit.
func NewProvider(ctx context.Context, cfg Config, serviceName, version string) (*Provider, func(context.Context), error) {
	noop := func(context.Context) {}

	if !cfg.Enabled || cfg.Endpoint == "" {
		return &Provider{}, noop, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		),
		resource.WithOS(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, noop, fmt.Errorf("create resource: %w", err)
	}

	ep, err := parseEndpoint(cfg)
	if err != nil {
		return nil, noop, err
	}

	var shutdowns []func(context.Context) error
	p := &Provider{}

	if cfg.Traces {
		if err := p.setupTracing(ctx, ep, cfg, res, &shutdowns); err != nil {
			return nil, noop, err
		}
	}

	if cfg.Metrics {
		if err := p.setupMetrics(ctx, ep, res, &shutdowns); err != nil {
			return nil, noop, err
		}
	}

	if cfg.Logs {
		if err := p.setupLogging(ctx, ep, res, &shutdowns); err != nil {
			return nil, noop, err
		}
	}

	shutdown := func(ctx context.Context) {
		for _, fn := range shutdowns {
			_ = fn(ctx)
		}
	}

	return p, shutdown, nil
}

// parseEndpoint extracts host, path, and scheme from the configured endpoint URL.
func parseEndpoint(cfg Config) (*endpointConfig, error) {
	parsedURL, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint URL: %w", err)
	}

	headers := make(map[string]string)
	if cfg.AuthToken != "" {
		headers["Authorization"] = "Basic " + cfg.AuthToken
	}

	return &endpointConfig{
		host:     parsedURL.Host,
		basePath: strings.TrimSuffix(parsedURL.Path, "/"),
		insecure: parsedURL.Scheme == "http",
		headers:  headers,
	}, nil
}

// setupTracing initializes the trace provider with an OTLP/HTTP exporter.
func (p *Provider) setupTracing(ctx context.Context, ep *endpointConfig, cfg Config, res *resource.Resource, shutdowns *[]func(context.Context) error) error {
	traceOpts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(ep.host),
		otlptracehttp.WithHeaders(ep.headers),
	}
	if ep.basePath != "" {
		traceOpts = append(traceOpts, otlptracehttp.WithURLPath(ep.basePath+"/v1/traces"))
	}
	if ep.insecure {
		traceOpts = append(traceOpts, otlptracehttp.WithInsecure())
	}

	traceExp, err := otlptracehttp.New(ctx, traceOpts...)
	if err != nil {
		return fmt.Errorf("create trace exporter: %w", err)
	}

	// 0 = never sample (disabled), (0,1) = ratio-based, >= 1 = always sample.
	sampler := trace.AlwaysSample()
	if cfg.TraceSampleRate == 0 {
		sampler = trace.NeverSample()
	} else if cfg.TraceSampleRate < 1.0 {
		sampler = trace.TraceIDRatioBased(cfg.TraceSampleRate)
	}

	tp := trace.NewTracerProvider(
		trace.WithBatcher(traceExp),
		trace.WithResource(res),
		trace.WithSampler(sampler),
	)
	otel.SetTracerProvider(tp)
	p.TracerProvider = tp
	*shutdowns = append(*shutdowns, tp.Shutdown)
	return nil
}

// setupMetrics initializes the meter provider with an OTLP/HTTP exporter.
func (p *Provider) setupMetrics(ctx context.Context, ep *endpointConfig, res *resource.Resource, shutdowns *[]func(context.Context) error) error {
	metricOpts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(ep.host),
		otlpmetrichttp.WithHeaders(ep.headers),
	}
	if ep.basePath != "" {
		metricOpts = append(metricOpts, otlpmetrichttp.WithURLPath(ep.basePath+"/v1/metrics"))
	}
	if ep.insecure {
		metricOpts = append(metricOpts, otlpmetrichttp.WithInsecure())
	}

	metricExp, err := otlpmetrichttp.New(ctx, metricOpts...)
	if err != nil {
		return fmt.Errorf("create metric exporter: %w", err)
	}

	mp := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExp)),
		metric.WithResource(res),
	)
	otel.SetMeterProvider(mp)
	p.MeterProvider = mp
	*shutdowns = append(*shutdowns, mp.Shutdown)
	return nil
}

// setupLogging initializes the log provider with an OTLP/HTTP exporter.
func (p *Provider) setupLogging(ctx context.Context, ep *endpointConfig, res *resource.Resource, shutdowns *[]func(context.Context) error) error {
	logOpts := []otlploghttp.Option{
		otlploghttp.WithEndpoint(ep.host),
		otlploghttp.WithHeaders(ep.headers),
	}
	if ep.basePath != "" {
		logOpts = append(logOpts, otlploghttp.WithURLPath(ep.basePath+"/v1/logs"))
	}
	if ep.insecure {
		logOpts = append(logOpts, otlploghttp.WithInsecure())
	}

	logExp, err := otlploghttp.New(ctx, logOpts...)
	if err != nil {
		return fmt.Errorf("create log exporter: %w", err)
	}

	lp := otellog.NewLoggerProvider(
		otellog.WithProcessor(otellog.NewBatchProcessor(logExp)),
		otellog.WithResource(res),
	)
	p.LogProvider = lp
	*shutdowns = append(*shutdowns, lp.Shutdown)
	return nil
}
