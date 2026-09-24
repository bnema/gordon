package logexport

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func activeApp(app string, stopped bool, services map[string]domain.AppEffectiveService) domain.AppActive {
	return domain.AppActive{App: app, StopIntent: stopped, Services: services}
}

func svc(container string, disabled bool) domain.AppEffectiveService {
	return domain.AppEffectiveService{Container: container, Spec: domain.AppService{LogExportDisabled: disabled}}
}

// blockingStreamer blocks each follower until canceled and records calls.
type blockingStreamer struct {
	mu      sync.Mutex
	started map[string]int
	stopped map[string]int
}

func newBlockingStreamer() *blockingStreamer {
	return &blockingStreamer{started: map[string]int{}, stopped: map[string]int{}}
}

func (s *blockingStreamer) StreamContainerLogs(ctx context.Context, containerID string, _ time.Time, _ func(domain.ContainerLogLine)) error {
	s.mu.Lock()
	s.started[containerID]++
	s.mu.Unlock()
	<-ctx.Done()
	s.mu.Lock()
	s.stopped[containerID]++
	s.mu.Unlock()
	return ctx.Err()
}

func (s *blockingStreamer) counts(containerID string) (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started[containerID], s.stopped[containerID]
}

func TestCollector_FollowsOnlyExportableContainers(t *testing.T) {
	state := mocks.NewMockAppStateReader(t)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog", "paused"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(activeApp("blog", false, map[string]domain.AppEffectiveService{
		"web": svc("c-web", false),
		"db":  svc("c-db", true), // service opted out
		"new": svc("", false),    // no container yet
	}), true, nil)
	state.EXPECT().LoadActive(mock.Anything, "paused").Return(activeApp("paused", true, map[string]domain.AppEffectiveService{
		"web": svc("c-paused", false),
	}), true, nil)
	streamer := newBlockingStreamer()
	c := NewCollector(state, streamer, mocks.NewMockLogExporter(t))

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, c.Reconcile(ctx))
	require.Eventually(t, func() bool { started, _ := streamer.counts("c-web"); return started == 1 }, time.Second, time.Millisecond)
	cancel()
	c.stopAll()

	for _, id := range []string{"c-db", "c-paused"} {
		started, _ := streamer.counts(id)
		assert.Zero(t, started, id)
	}
	_, stopped := streamer.counts("c-web")
	assert.Equal(t, 1, stopped, "stopAll waits for followers")
}

func TestCollector_StopsFollowerWhenServiceOptsOut(t *testing.T) {
	state := mocks.NewMockAppStateReader(t)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(activeApp("blog", false, map[string]domain.AppEffectiveService{
		"web": svc("c-web", false),
	}), true, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(activeApp("blog", false, map[string]domain.AppEffectiveService{
		"web": svc("c-web", true),
	}), true, nil).Once()
	streamer := newBlockingStreamer()
	c := NewCollector(state, streamer, mocks.NewMockLogExporter(t))
	defer c.stopAll()

	require.NoError(t, c.Reconcile(context.Background()))
	require.NoError(t, c.Reconcile(context.Background()))

	require.Eventually(t, func() bool { _, stopped := streamer.counts("c-web"); return stopped == 1 }, time.Second, time.Millisecond)
}

func TestCollector_ExportsLinesWithSourceIdentity(t *testing.T) {
	state := mocks.NewMockAppStateReader(t)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(activeApp("blog", false, map[string]domain.AppEffectiveService{
		"web": svc("c-web", false),
	}), true, nil)

	ts := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	streamer := mocks.NewMockContainerLogStreamer(t)
	streamer.EXPECT().StreamContainerLogs(mock.Anything, "c-web", mock.Anything, mock.Anything).
		RunAndReturn(func(ctx context.Context, _ string, _ time.Time, emit func(domain.ContainerLogLine)) error {
			emit(domain.ContainerLogLine{Time: ts, Stream: domain.LogStreamStderr, Body: "boom"})
			<-ctx.Done()
			return ctx.Err()
		})

	exported := make(chan domain.LogRecord, 1)
	exporter := mocks.NewMockLogExporter(t)
	exporter.EXPECT().Export(mock.Anything, mock.Anything).Run(func(_ context.Context, r domain.LogRecord) {
		exported <- r
	}).Return()

	c := NewCollector(state, streamer, exporter)
	defer c.stopAll()
	require.NoError(t, c.Reconcile(context.Background()))

	select {
	case record := <-exported:
		assert.Equal(t, domain.LogRecord{
			Time:   ts,
			Source: domain.LogSource{App: "blog", Service: "web"},
			Type:   domain.LogTypeContainer,
			Stream: domain.LogStreamStderr,
			Body:   "boom",
		}, record)
		assert.Equal(t, "blog.web", record.Source.ServiceName())
	case <-time.After(time.Second):
		t.Fatal("no record exported")
	}
}

func TestCollector_ResumesAfterLastLineOnReconnect(t *testing.T) {
	state := mocks.NewMockAppStateReader(t)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(activeApp("blog", false, map[string]domain.AppEffectiveService{
		"web": svc("c-web", false),
	}), true, nil)

	start := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	last := start.Add(time.Minute)
	streamer := mocks.NewMockContainerLogStreamer(t)
	streamer.EXPECT().StreamContainerLogs(mock.Anything, "c-web", start, mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, _ time.Time, emit func(domain.ContainerLogLine)) error {
			emit(domain.ContainerLogLine{Time: last, Body: "line"})
			return nil // stream ended: container restarting
		}).Once()
	resumed := make(chan time.Time, 1)
	streamer.EXPECT().StreamContainerLogs(mock.Anything, "c-web", mock.Anything, mock.Anything).
		RunAndReturn(func(ctx context.Context, _ string, since time.Time, _ func(domain.ContainerLogLine)) error {
			resumed <- since
			<-ctx.Done()
			return ctx.Err()
		}).Once()
	exporter := mocks.NewMockLogExporter(t)
	exporter.EXPECT().Export(mock.Anything, mock.Anything).Return()

	c := NewCollector(state, streamer, exporter)
	c.startedAt = start
	c.retryDelay = time.Millisecond
	defer c.stopAll()
	require.NoError(t, c.Reconcile(context.Background()))

	select {
	case since := <-resumed:
		assert.Equal(t, last.Add(time.Nanosecond), since)
	case <-time.After(time.Second):
		t.Fatal("follower did not reconnect")
	}
}
