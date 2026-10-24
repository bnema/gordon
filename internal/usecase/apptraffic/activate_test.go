package apptraffic_test

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apptraffic"
)

type stubState struct {
	intents map[string]domain.AppStopIntent
	actives map[string]domain.AppActive
}

func (s stubState) ListApps(_ context.Context) ([]string, error) {
	apps := make([]string, 0, len(s.actives)+len(s.intents))
	seen := map[string]bool{}
	for app := range s.actives {
		if !seen[app] {
			seen[app] = true
			apps = append(apps, app)
		}
	}
	for app := range s.intents {
		if !seen[app] {
			seen[app] = true
			apps = append(apps, app)
		}
	}
	return apps, nil
}

func (s stubState) LoadIntent(_ context.Context, app string) (domain.AppStopIntent, error) {
	return s.intents[app], nil
}

func (s stubState) LoadActive(_ context.Context, app string) (domain.AppActive, bool, error) {
	active, ok := s.actives[app]
	return active, ok, nil
}

func TestActivator_RebuildHostIndex(t *testing.T) {
	ctx := context.Background()
	activator := apptraffic.NewActivator(zerowrap.Default())
	index := apptraffic.NewHostIndex()

	served := domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"web": {
				EffectiveRevision: "rev-1", Image: "img:1",
				Spec: domain.AppService{
					HTTP: []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8080, TLS: "auto"}},
				},
			},
		},
	}
	stopped := domain.AppActive{
		App: "old",
		Services: map[string]domain.AppEffectiveService{
			"web": {
				EffectiveRevision: "rev-1", Image: "img:1",
				Spec: domain.AppService{
					HTTP: []domain.AppHTTPInterface{{Host: "old.example.com", Port: 8080, TLS: "auto"}},
				},
			},
		},
	}
	state := stubState{
		actives: map[string]domain.AppActive{"blog": served, "old": stopped},
		intents: map[string]domain.AppStopIntent{"old": {Stopped: true}},
	}

	require.NoError(t, activator.RebuildHostIndex(ctx, index, state, nil, nil))

	_, ok := index.Lookup("blog.example.com")
	assert.True(t, ok)
	_, ok = index.Lookup("old.example.com")
	assert.False(t, ok, "STOPPED-intent app must not stay routable")
}

func TestRebuildHostIndexPrunesPublishedHold(t *testing.T) {
	ctx := context.Background()
	activator := apptraffic.NewActivator(zerowrap.Default())
	index := apptraffic.NewHostIndex()
	holds := apptraffic.NewHolds()
	deadline := time.Now().Add(time.Minute)
	holds.Set("blog", "web", deadline)
	active := webActive()
	svc := active.Services["web"]
	svc.BackendBinds = nil
	active.Services["web"] = svc
	state := stubState{actives: map[string]domain.AppActive{"blog": active}}
	require.NoError(t, activator.RebuildHostIndex(ctx, index, state, nil, holds))
	_, until, ok := index.LookupRoute("blog.example.com")
	require.True(t, ok)
	require.Equal(t, deadline, until)
	svc.BackendBinds = map[int]int{8080: 18080}
	active.Services["web"] = svc
	require.NoError(t, activator.RebuildHostIndex(ctx, index, state, nil, holds))
	_, ok = holds.Until("blog", "web")
	require.False(t, ok)
	svc.BackendBinds = nil
	active.Services["web"] = svc
	require.NoError(t, activator.RebuildHostIndex(ctx, index, state, nil, holds))
	_, until, ok = index.LookupRoute("blog.example.com")
	require.True(t, ok)
	require.True(t, until.IsZero())
	require.Empty(t, index.AppHosts())
}
