package logexport

import (
	"context"
	"errors"
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

func TestCollector_ReconcileWrapsStateErrors(t *testing.T) {
	listErr := assert.AnError
	state := mocks.NewMockAppStateReader(t)
	state.EXPECT().ListApps(mock.Anything).Return(nil, listErr)
	c := NewCollector(state, newBlockingStreamer(), mocks.NewMockLogExporter(t))
	defer c.stopAll()

	err := c.Reconcile(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, listErr)

	loadErr := assert.AnError
	state2 := mocks.NewMockAppStateReader(t)
	state2.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil)
	state2.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{}, false, loadErr)
	c2 := NewCollector(state2, newBlockingStreamer(), mocks.NewMockLogExporter(t))
	defer c2.stopAll()

	err = c2.Reconcile(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, loadErr)
}

func TestCollector_RecreatedFollowResumesFromSavedCursor(t *testing.T) {
	state := mocks.NewMockAppStateReader(t)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(activeApp("blog", false, map[string]domain.AppEffectiveService{
		"web": svc("c-web", false),
	}), true, nil)

	start := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	last := start.Add(time.Minute)
	streamed := make(chan time.Time, 4)
	streamer := mocks.NewMockContainerLogStreamer(t)
	first := make(chan struct{})
	// The first stream emits one line then ends (container restart);
	// later streams only record their resume point.
	streamer.EXPECT().StreamContainerLogs(mock.Anything, "c-web", mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, since time.Time, emit func(domain.ContainerLogLine)) error {
			select {
			case streamed <- since:
			default:
			}
			select {
			case <-first:
				return errors.New("boom")
			default:
				close(first)
				emit(domain.ContainerLogLine{Time: last, Body: "line"})
				return nil
			}
		}).Maybe()
	exporter := mocks.NewMockLogExporter(t)
	exporter.EXPECT().Export(mock.Anything, mock.Anything).Return().Maybe()

	c := NewCollector(state, streamer, exporter)
	c.startedAt = start
	c.retryDelay = 50 * time.Millisecond
	defer c.stopAll()
	require.NoError(t, c.Reconcile(context.Background()))

	select {
	case since := <-streamed:
		assert.Equal(t, start, since, "first follow starts at collector start")
	case <-time.After(2 * time.Second):
		t.Fatal("first follow did not start")
	}

	// The emitted line must advance the saved cursor past startedAt.
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		cursor, ok := c.cursors["c-web"]
		return ok && cursor.Equal(last.Add(time.Nanosecond))
	}, 2*time.Second, 10*time.Millisecond, "cursor must advance past the exported line")

	// Simulate the supervision loop recreating the follow (source
	// reassignment): the new follow must resume from the saved cursor.
	c.mu.Lock()
	if f, ok := c.followers["c-web"]; ok {
		f.cancel()
		delete(c.followers, "c-web")
	}
	c.mu.Unlock()
	require.NoError(t, c.Reconcile(context.Background()))

	select {
	case since := <-streamed:
		assert.Equal(t, last.Add(time.Nanosecond), since, "recreated follow resumes from the saved cursor")
	case <-time.After(2 * time.Second):
		t.Fatal("recreated follow did not start")
	}
}

func TestCollector_RetiresCursorWhenIDLeavesActive(t *testing.T) {
	state := mocks.NewMockAppStateReader(t)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Times(2)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(activeApp("blog", false, map[string]domain.AppEffectiveService{
		"web": svc("c-web", false),
	}), true, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(activeApp("blog", false, map[string]domain.AppEffectiveService{
		"web": svc("c-new", false),
	}), true, nil).Once()
	streamer := newBlockingStreamer()
	c := NewCollector(state, streamer, mocks.NewMockLogExporter(t))
	defer c.stopAll()

	require.NoError(t, c.Reconcile(context.Background()))
	c.mu.Lock()
	c.cursors["c-web"] = time.Now()
	c.mu.Unlock()
	require.NoError(t, c.Reconcile(context.Background()))

	c.mu.Lock()
	_, ok := c.cursors["c-web"]
	c.mu.Unlock()
	assert.False(t, ok, "cursor is dropped once the ID is gone from ACTIVE services")
}
