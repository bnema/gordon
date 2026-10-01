package telemetry

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/bnema/gordon/internal/domain"
)

type recordingExporter struct {
	mu       sync.Mutex
	records  []sdklog.Record
	shutdown int
}

func (e *recordingExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range records {
		e.records = append(e.records, r.Clone())
	}
	return nil
}

func (e *recordingExporter) Shutdown(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.shutdown++
	return nil
}

func (e *recordingExporter) ForceFlush(context.Context) error { return nil }

func resourceAttr(r sdklog.Record, key string) string {
	res := r.Resource()
	value, _ := res.Set().Value(attribute.Key(key))
	return value.AsString()
}

func recordAttr(r sdklog.Record, key string) string {
	var found string
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		if string(kv.Key) == key {
			found = kv.Value.AsString()
			return false
		}
		return true
	})
	return found
}

func TestLogExporter_ExportsPerSourceIdentity(t *testing.T) {
	rec := &recordingExporter{}
	base := resource.NewSchemaless(attribute.String("service.name", "gordon"), attribute.String("host.name", "node-1"))
	exp := NewLogExporter(rec, base)
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	exp.Export(context.Background(), domain.LogRecord{
		Time:     ts,
		Source:   domain.LogSource{App: "blog", Service: "web"},
		Type:     domain.LogTypeContainer,
		Stream:   domain.LogStreamStderr,
		Severity: domain.LogSeverityError,
		Body:     "boom",
	})
	exp.Export(context.Background(), domain.LogRecord{
		Time:   ts,
		Source: domain.LogSource{App: "shop", Service: "web"},
		Body:   "hello",
	})
	require.NoError(t, exp.Shutdown(context.Background()))

	require.Len(t, rec.records, 2)
	byService := map[string]sdklog.Record{}
	for _, r := range rec.records {
		byService[resourceAttr(r, "service.name")] = r
	}
	blog, ok := byService["blog.web"]
	require.True(t, ok, "services with the same name stay distinct per app")
	_, ok = byService["shop.web"]
	require.True(t, ok)

	assert.Equal(t, "blog", resourceAttr(blog, "service.namespace"))
	assert.Equal(t, "blog", resourceAttr(blog, attrGordonApp))
	assert.Equal(t, "web", resourceAttr(blog, attrGordonService))
	assert.Equal(t, "node-1", resourceAttr(blog, "host.name"), "base attributes are kept")
	assert.Equal(t, "boom", blog.Body().AsString())
	assert.Equal(t, ts, blog.Timestamp())
	assert.Equal(t, otellog.SeverityError, blog.Severity())
	assert.Equal(t, domain.LogTypeContainer, recordAttr(blog, attrLogType))
	assert.Equal(t, domain.LogStreamStderr, recordAttr(blog, attrLogStream))
	assert.Equal(t, 1, rec.shutdown, "shared exporter is shut down exactly once")
}

func TestLogExporter_DropsAfterShutdown(t *testing.T) {
	rec := &recordingExporter{}
	exp := NewLogExporter(rec, nil)
	require.NoError(t, exp.Shutdown(context.Background()))

	exp.Export(context.Background(), domain.LogRecord{Source: domain.LogSource{App: "blog", Service: "web"}, Body: "late"})

	assert.Empty(t, rec.records)
}
