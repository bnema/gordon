package apps_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apps"
)

// fakeAppStateReader is an in-memory AppStateReader for lifecycle scenarios.
type fakeAppStateReader struct {
	active  map[string]domain.AppActive
	stopped map[string]bool
	listErr error
}

func newFakeAppStateReader() *fakeAppStateReader {
	return &fakeAppStateReader{active: map[string]domain.AppActive{}, stopped: map[string]bool{}}
}

func (f *fakeAppStateReader) ListApps(context.Context) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	names := make([]string, 0, len(f.active))
	for name := range f.active {
		names = append(names, name)
	}
	return names, nil
}

func (f *fakeAppStateReader) LoadActive(_ context.Context, app string) (domain.AppActive, bool, error) {
	a, ok := f.active[app]
	return a, ok, nil
}

func (f *fakeAppStateReader) LoadIntent(_ context.Context, app string) (domain.AppStopIntent, error) {
	return domain.AppStopIntent{App: app, Stopped: f.stopped[app]}, nil
}

func activeWith(app string, containers ...string) domain.AppActive {
	services := map[string]domain.AppEffectiveService{}
	for i, c := range containers {
		services[string(rune('a'+i))] = domain.AppEffectiveService{Container: c}
	}
	return domain.AppActive{App: app, Services: services}
}

func TestCountManagedContainers_Lifecycle(t *testing.T) {
	ctx := context.Background()
	state := newFakeAppStateReader()
	count := func() int64 {
		t.Helper()
		n, err := apps.CountManagedContainers(ctx, state)
		require.NoError(t, err)
		return n
	}

	// Boot with no apps.
	assert.Equal(t, int64(0), count())

	// Boot hydration: persisted ACTIVE state is counted immediately.
	state.active["web"] = activeWith("web", "c1", "c2")
	state.active["db"] = activeWith("db", "c3")
	assert.Equal(t, int64(3), count())

	// Failed deploy of a new service leaves it without a container.
	state.active["web"] = activeWith("web", "c1", "c2", "")
	assert.Equal(t, int64(3), count())

	// Stop excludes the app; start restores it.
	state.stopped["web"] = true
	assert.Equal(t, int64(1), count())
	state.stopped["web"] = false
	assert.Equal(t, int64(3), count())

	// Remove retires the app state.
	delete(state.active, "db")
	assert.Equal(t, int64(2), count())
}

func TestCountManagedContainers_Error(t *testing.T) {
	state := newFakeAppStateReader()
	state.listErr = errors.New("boom")
	_, err := apps.CountManagedContainers(context.Background(), state)
	require.ErrorContains(t, err, "boom")
}
