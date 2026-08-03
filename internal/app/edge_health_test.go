package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"
	edgesnapshotclient "github.com/bnema/gordon/internal/adapters/out/grpc/edgesnapshot"
	"github.com/bnema/gordon/internal/domain"
)

type edgeHealthRoutes struct{ health edgesnapshotclient.Health }

func (r edgeHealthRoutes) SnapshotHealth() edgesnapshotclient.Health { return r.health }

type edgeHealthGraphs struct {
	health edgesnapshotclient.TrafficGraphHealth
}

func (g *edgeHealthGraphs) CurrentTrafficGraph(context.Context) (domain.TrafficGraphSnapshot, error) {
	return domain.TrafficGraphSnapshot{}, nil
}
func (g *edgeHealthGraphs) SetTrafficGraphAcceptanceObserver(func(domain.TrafficGraphSnapshot)) {}
func (g *edgeHealthGraphs) TrafficGraphHealth() edgesnapshotclient.TrafficGraphHealth {
	return g.health
}

type edgeHealthManager struct{ status domain.TrafficStatus }

func (m edgeHealthManager) ApplyWithServers(context.Context, *domain.TrafficGraph, trafficadapter.ServerConfigBundle) error {
	return nil
}
func (m edgeHealthManager) Status() domain.TrafficStatus   { return m.status }
func (m edgeHealthManager) Shutdown(context.Context) error { return nil }

func TestEdgeAggregateHealthRequiresCurrentStreamsAndAppliedTraffic(t *testing.T) {
	graphs := &edgeHealthGraphs{health: edgesnapshotclient.TrafficGraphHealth{Healthy: true, LastAcceptedGeneration: 2}}
	manager := edgeHealthManager{status: domain.TrafficStatus{LastReloadStatus: "ok"}}
	health := newEdgeAggregateHealth(edgeHealthRoutes{health: edgesnapshotclient.Health{Healthy: true}}, graphs, manager, nil)
	health.beginApply(1)
	health.completeApply(1, nil)
	assert.False(t, health.healthy(), "a newer accepted graph must be applied before readiness")

	health.beginApply(2)
	health.completeApply(2, assert.AnError)
	assert.False(t, health.healthy(), "a rejected update must make readiness fail")
	health.completeApply(2, nil)
	assert.True(t, health.healthy())

	graphs.health.Healthy = false
	assert.False(t, health.healthy(), "a disconnected traffic stream must make readiness fail")
}

func TestEdgeHealthzDoesNotExposeFailureDetails(t *testing.T) {
	graphs := &edgeHealthGraphs{health: edgesnapshotclient.TrafficGraphHealth{Healthy: true, LastAcceptedGeneration: 1}}
	health := newEdgeAggregateHealth(edgeHealthRoutes{health: edgesnapshotclient.Health{Healthy: true}}, graphs, edgeHealthManager{status: domain.TrafficStatus{LastReloadStatus: "error", LastReloadError: "certificate path /secret failed"}}, nil)
	health.beginApply(1)
	health.completeApply(1, nil)

	response := httptest.NewRecorder()
	edgeHTTPHandlerWithHealth(http.NotFoundHandler(), nil, health).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
	assert.NotContains(t, response.Body.String(), "secret")
}
