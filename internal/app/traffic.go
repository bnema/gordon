package app

import (
	"context"
	"fmt"

	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
	proxyusecase "github.com/bnema/gordon/internal/usecase/proxy"
	servicecfg "github.com/bnema/gordon/internal/usecase/services"
	trafficbuilder "github.com/bnema/gordon/internal/usecase/traffic"
)

func applyTrafficRuntimeConfig(ctx context.Context, manager *trafficadapter.Manager, cfg Config, configSvc in.ConfigService, proxySvc *proxyusecase.Service, serviceSvcs ...in.StandaloneServiceService) error {
	if configSvc == nil {
		return nil
	}
	standaloneServices, err := servicecfg.ToDomain(cfg.Services)
	if err != nil {
		return fmt.Errorf("convert service config: %w", err)
	}
	input := trafficbuilder.Input{
		EntryPoints:     cfg.EntryPoints,
		Traffic:         cfg.Traffic,
		Routes:          configSvc.GetRoutes(ctx),
		ExternalRoutes:  configSvc.GetExternalRoutes(),
		ServiceRoutes:   configSvc.GetServiceRoutes(),
		NetworkServices: cfg.NetworkServices,
		Services:        standaloneServices,
	}
	if err := trafficbuilder.Validate(input); err != nil {
		return fmt.Errorf("validate traffic graph: %w", err)
	}
	if len(serviceSvcs) > 0 && serviceSvcs[0] != nil {
		if err := serviceSvcs[0].Reconcile(ctx, standaloneServices); err != nil {
			return fmt.Errorf("reconcile services: %w", err)
		}
		input.ServiceBackends, err = serviceSvcs[0].ResolvePorts(ctx, standaloneServices)
		if err != nil {
			return fmt.Errorf("resolve service ports: %w", err)
		}
	}
	plan, err := trafficbuilder.BuildPlan(input)
	if err != nil {
		return fmt.Errorf("build traffic graph: %w", err)
	}
	if manager != nil {
		owned, err := trafficRuntimeGraph(plan.Graph)
		if err != nil {
			return fmt.Errorf("filter traffic graph for runtime ownership: %w", err)
		}
		if err := manager.Apply(ctx, &owned); err != nil {
			return fmt.Errorf("apply traffic graph: %w", err)
		}
	}
	if proxySvc != nil {
		if err := proxySvc.StageServiceTargets(plan.ServiceTargets); err != nil {
			return fmt.Errorf("stage HTTP service targets: %w", err)
		}
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
