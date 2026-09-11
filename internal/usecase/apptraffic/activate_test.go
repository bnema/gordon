package apptraffic_test

import (
	"context"
	"testing"

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

	require.NoError(t, activator.RebuildHostIndex(ctx, index, state, nil))

	_, ok := index.Lookup("blog.example.com")
	assert.True(t, ok)
	_, ok = index.Lookup("old.example.com")
	assert.False(t, ok, "STOPPED-intent app must not stay routable")
}
