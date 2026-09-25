package logexport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

type fakeHosts map[string][2]string

func (f fakeHosts) HostOwner(host string) (string, string, bool) {
	owner, ok := f[host]
	return owner[0], owner[1], ok
}

type fakeAccessWriter struct {
	entries []out.AccessLogEntry
	err     error
}

func (f *fakeAccessWriter) Write(entry out.AccessLogEntry) error {
	f.entries = append(f.entries, entry)
	return f.err
}

func captureExport(t *testing.T) (*mocks.MockLogExporter, *[]domain.LogRecord) {
	var records []domain.LogRecord
	exporter := mocks.NewMockLogExporter(t)
	exporter.EXPECT().Export(mock.Anything, mock.Anything).Run(func(_ context.Context, r domain.LogRecord) {
		records = append(records, r)
	}).Return()
	return exporter, &records
}

func TestAccessLogExporter_AttributesOwnedHostToAppService(t *testing.T) {
	exporter, records := captureExport(t)
	next := &fakeAccessWriter{}
	a := NewAccessLogExporter(fakeHosts{"blog.example.com": {"blog", "web"}}, exporter, next)
	ts := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	err := a.Write(out.AccessLogEntry{Time: ts, Method: "GET", Host: "Blog.Example.com:443", Path: "/x", Status: 502})

	require.NoError(t, err)
	require.Len(t, *records, 1)
	record := (*records)[0]
	assert.Equal(t, domain.LogSource{App: "blog", Service: "web"}, record.Source)
	assert.Equal(t, domain.LogTypeAccess, record.Type)
	assert.Equal(t, domain.LogSeverityError, record.Severity)
	assert.Equal(t, ts, record.Time)
	assert.Equal(t, "GET blog.example.com/x 502", record.Body)
	assert.Equal(t, "502", record.Attributes["http.response.status_code"])
	assert.Len(t, next.entries, 1, "local sink still receives the entry")
}

func TestAccessLogExporter_UnownedHostIsGordon(t *testing.T) {
	exporter, records := captureExport(t)
	a := NewAccessLogExporter(fakeHosts{}, exporter, nil)

	require.NoError(t, a.Write(out.AccessLogEntry{Host: "registry.example.com", Status: 200}))

	require.Len(t, *records, 1)
	assert.True(t, (*records)[0].Source.IsGordon())
	assert.Equal(t, domain.LogSeverityInfo, (*records)[0].Severity)
}

func TestAccessLogExporter_ReturnsLocalSinkError(t *testing.T) {
	exporter, _ := captureExport(t)
	sinkErr := errors.New("disk full")
	a := NewAccessLogExporter(fakeHosts{}, exporter, &fakeAccessWriter{err: sinkErr})

	assert.ErrorIs(t, a.Write(out.AccessLogEntry{Status: 404}), sinkErr)
}

func TestAccessLogExporter_DropsQueryAttribute(t *testing.T) {
	exporter, records := captureExport(t)
	a := NewAccessLogExporter(fakeHosts{}, exporter, nil)

	require.NoError(t, a.Write(out.AccessLogEntry{Path: "/search", Query: "q=secret", Status: 200}))

	require.Len(t, *records, 1)
	_, ok := (*records)[0].Attributes["url.query"]
	assert.False(t, ok, "query must not be exported over OTLP")
	assert.Equal(t, "/search", (*records)[0].Attributes["url.path"])
}

func TestAccessLogExporter_SanitizesInvalidUTF8(t *testing.T) {
	exporter, records := captureExport(t)
	a := NewAccessLogExporter(fakeHosts{}, exporter, nil)

	require.NoError(t, a.Write(out.AccessLogEntry{Path: "/bad\xff", UserAgent: "agent\xff", Status: 200}))

	require.Len(t, *records, 1)
	assert.Equal(t, "/bad\uFFFD", (*records)[0].Attributes["url.path"])
	assert.Equal(t, "agent\uFFFD", (*records)[0].Attributes["user_agent.original"])
}
