package app

import (
	"context"
	"fmt"

	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
	trafficbuilder "github.com/bnema/gordon/internal/usecase/traffic"
)

func applyTrafficRuntimeConfig(ctx context.Context, manager *trafficadapter.Manager, cfg Config, configSvc in.ConfigService) error {
	if manager == nil || configSvc == nil {
		return nil
	}
	graph, err := trafficbuilder.Build(trafficbuilder.Input{
		EntryPoints:     cfg.EntryPoints,
		Traffic:         cfg.Traffic,
		Routes:          configSvc.GetRoutes(ctx),
		ExternalRoutes:  configSvc.GetExternalRoutes(),
		NetworkServices: cfg.NetworkServices,
	})
	if err != nil {
		return fmt.Errorf("build traffic graph: %w", err)
	}
	owned, err := trafficRuntimeGraph(graph)
	if err != nil {
		return fmt.Errorf("filter traffic graph for runtime ownership: %w", err)
	}
	if err := manager.Apply(ctx, &owned); err != nil {
		return fmt.Errorf("apply traffic graph: %w", err)
	}
	return nil
}

func trafficRuntimeGraph(graph domain.TrafficGraph) (domain.TrafficGraph, error) {
	ownedEntryPoints := map[string]struct{}{}
	filtered := domain.TrafficGraph{Options: graph.Options}
	for _, entryPoint := range graph.EntryPoints {
		if trafficManagerOwnsEntryPoint(entryPoint) {
			filtered.EntryPoints = append(filtered.EntryPoints, entryPoint)
			ownedEntryPoints[entryPoint.Name] = struct{}{}
		}
	}

	usedServices := map[string]struct{}{}
	for _, router := range graph.Routers {
		if _, ok := ownedEntryPoints[router.EntryPoint]; !ok {
			if trafficManagerRouterProtocol(router.Protocol) {
				return domain.TrafficGraph{}, fmt.Errorf("traffic router %q targets entrypoint %q, which is not owned by the traffic manager", router.Name, router.EntryPoint)
			}
			continue
		}
		filtered.Routers = append(filtered.Routers, router)
		usedServices[router.Service] = struct{}{}
	}
	for _, service := range graph.Services {
		if _, ok := usedServices[service.Name]; ok {
			filtered.Services = append(filtered.Services, service)
		}
	}
	return filtered, nil
}

func trafficManagerRouterProtocol(protocol domain.RouterProtocol) bool {
	switch protocol {
	case domain.RouterProtocolTCP, domain.RouterProtocolUDP, domain.RouterProtocolTLSPassthrough:
		return true
	default:
		return false
	}
}

func trafficManagerOwnsEntryPoint(entryPoint domain.EntryPoint) bool {
	switch entryPoint.Protocol {
	case domain.EntryPointProtocolTLSMux, domain.EntryPointProtocolSmartTCP, domain.EntryPointProtocolTCP, domain.EntryPointProtocolUDP:
		return true
	default:
		return false
	}
}
