package health

import (
	"context"
	"errors"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func testActive() domain.AppActive {
	return domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"web": {
				EffectiveRevision: "rev-1",
				Image:             "img:1",
				Container:         "c-web",
				BackendBinds:      map[int]int{8080: 18080},
				Spec: domain.AppService{
					HTTP: []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8080, TLS: "auto"}},
				},
			},
		},
	}
}

func TestService_CheckAllRoutes_Healthy(t *testing.T) {
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	prober := inmocks.NewMockHTTPProber(t)

	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil)
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(testActive(), true, nil)
	runtime.EXPECT().IsContainerRunning(mock.Anything, "c-web").Return(true, nil)
	prober.EXPECT().Probe(mock.Anything, "http://127.0.0.1:18080/").Return(200, int64(45), nil)

	svc := NewService(state, runtime, prober, testLogger())

	results := svc.CheckAllRoutes(context.Background())

	assert.Len(t, results, 1)
	health := results["blog.example.com"]
	assert.Equal(t, "blog.example.com", health.Domain)
	assert.Equal(t, "running", health.ContainerStatus)
	assert.Equal(t, 200, health.HTTPStatus)
	assert.Equal(t, int64(45), health.ResponseTimeMs)
	assert.True(t, health.Healthy)
	assert.Empty(t, health.Error)
}

func TestService_CheckAllRoutes_ContainerNotRunning(t *testing.T) {
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	prober := inmocks.NewMockHTTPProber(t)

	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil)
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(testActive(), true, nil)
	runtime.EXPECT().IsContainerRunning(mock.Anything, "c-web").Return(false, nil)

	svc := NewService(state, runtime, prober, testLogger())

	results := svc.CheckAllRoutes(context.Background())

	assert.Len(t, results, 1)
	assert.Equal(t, "not running", results["blog.example.com"].ContainerStatus)
	assert.False(t, results["blog.example.com"].Healthy)
}

func TestService_CheckAllRoutes_ProbeFailure(t *testing.T) {
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	prober := inmocks.NewMockHTTPProber(t)

	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil)
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(testActive(), true, nil)
	runtime.EXPECT().IsContainerRunning(mock.Anything, "c-web").Return(true, nil)
	prober.EXPECT().Probe(mock.Anything, "http://127.0.0.1:18080/").Return(0, int64(0), errors.New("connection refused"))

	svc := NewService(state, runtime, prober, testLogger())

	results := svc.CheckAllRoutes(context.Background())

	assert.Len(t, results, 1)
	assert.Equal(t, "running", results["blog.example.com"].ContainerStatus)
	assert.False(t, results["blog.example.com"].Healthy)
	assert.Equal(t, "connection refused", results["blog.example.com"].Error)
}

func TestService_CheckAllRoutes_StoppedIntentSkipped(t *testing.T) {
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	prober := inmocks.NewMockHTTPProber(t)

	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil)
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog", Stopped: true}, nil)

	svc := NewService(state, runtime, prober, testLogger())

	results := svc.CheckAllRoutes(context.Background())

	assert.Empty(t, results)
}

func TestService_CheckAllRoutes_NoApps(t *testing.T) {
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	prober := inmocks.NewMockHTTPProber(t)

	state.EXPECT().ListApps(mock.Anything).Return(nil, nil)

	svc := NewService(state, runtime, prober, testLogger())

	results := svc.CheckAllRoutes(context.Background())

	assert.Empty(t, results)
}
