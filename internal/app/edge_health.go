package app

import (
	"sync"

	edgesnapshotclient "github.com/bnema/gordon/internal/adapters/out/grpc/edgesnapshot"
	"github.com/bnema/gordon/internal/domain"
)

// edgeAggregateHealth combines the independently delivered route and traffic
// state. It deliberately exposes no underlying error details to public probes.
type edgeRouteHealthProvider interface {
	SnapshotHealth() edgesnapshotclient.Health
}

type edgeAggregateHealth struct {
	routes  edgeRouteHealthProvider
	graphs  edgeTrafficGraphProvider
	manager edgeTrafficManager
	tls     *edgeCertificateReloader

	mu                  sync.RWMutex
	initialized         bool
	attemptedGeneration domain.TrafficGraphGeneration
	appliedGeneration   domain.TrafficGraphGeneration
	applyHealthy        bool
}

func newEdgeAggregateHealth(routes edgeRouteHealthProvider, graphs edgeTrafficGraphProvider, manager edgeTrafficManager, tls *edgeCertificateReloader) *edgeAggregateHealth {
	return &edgeAggregateHealth{routes: routes, graphs: graphs, manager: manager, tls: tls}
}

func (h *edgeAggregateHealth) beginApply(generation domain.TrafficGraphGeneration) {
	h.mu.Lock()
	h.initialized, h.attemptedGeneration, h.applyHealthy = true, generation, false
	h.mu.Unlock()
}

func (h *edgeAggregateHealth) completeApply(generation domain.TrafficGraphGeneration, err error) {
	h.mu.Lock()
	if err == nil && generation == h.attemptedGeneration {
		h.appliedGeneration, h.applyHealthy = generation, true
	} else {
		h.applyHealthy = false
	}
	h.mu.Unlock()
}

func (h *edgeAggregateHealth) stop() {
	h.mu.Lock()
	h.initialized, h.applyHealthy = false, false
	h.mu.Unlock()
}

func (h *edgeAggregateHealth) healthy() bool {
	if h == nil || h.routes == nil || h.graphs == nil || h.manager == nil {
		return false
	}
	routeHealth := h.routes.SnapshotHealth()
	trafficHealth := h.graphs.TrafficGraphHealth()
	managerStatus := h.manager.Status()
	h.mu.RLock()
	initialized, applied, applyHealthy := h.initialized, h.appliedGeneration, h.applyHealthy
	h.mu.RUnlock()
	if !initialized || !applyHealthy || !routeHealth.Healthy || !trafficHealth.Healthy || applied != trafficHealth.LastAcceptedGeneration {
		return false
	}
	if managerStatus.LastReloadStatus != "ok" || managerStatus.LastReloadError != "" {
		return false
	}
	return h.tls == nil || h.tls.Healthy()
}
