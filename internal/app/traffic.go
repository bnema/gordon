package app

import (
	"context"
	"fmt"

	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apptraffic"
	servicecfg "github.com/bnema/gordon/internal/usecase/services"
	trafficbuilder "github.com/bnema/gordon/internal/usecase/traffic"
)

// appHostRoutes adapts ACTIVE-derived hosts to the traffic builder's
// route input: host-only entries, Image explicitly unused (no app
// meaning). Stopped-intent and unresolved hosts never reach the graph.
type appHostRoutes interface {
	AppHosts() []out.AppHost
	L4Entries() []apptraffic.RouteEntry
}

func applyTrafficRuntimeConfig(ctx context.Context, manager *trafficadapter.Manager, cfg Config, configSvc in.ConfigService, hosts appHostRoutes) error {
	if manager == nil || configSvc == nil {
		return nil
	}
	standaloneServices, err := servicecfg.ToDomain(cfg.Services)
	if err != nil {
		return fmt.Errorf("convert standalone service config: %w", err)
	}
	graph, err := trafficbuilder.Build(trafficbuilder.Input{
		EntryPoints:     cfg.EntryPoints,
		Traffic:         cfg.Traffic,
		Routes:          appHostDomainRoutes(hosts),
		ExternalRoutes:  configSvc.GetExternalRoutes(),
		NetworkServices: cfg.NetworkServices,
		Services:        standaloneServices,
	})
	if err != nil {
		return fmt.Errorf("build traffic graph: %w", err)
	}
	if err := addAppL4Routes(&graph, hosts); err != nil {
		return fmt.Errorf("add app L4 routes: %w", err)
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

func addAppL4Routes(graph *domain.TrafficGraph, routes appHostRoutes) error {
	if routes == nil {
		return nil
	}
	existing := map[string]struct{}{}
	for _, router := range graph.Routers {
		existing["router:"+router.Name] = struct{}{}
	}
	for _, service := range graph.Services {
		existing["service:"+service.Name] = struct{}{}
	}
	entrypointAddress := make(map[string]string, len(graph.EntryPoints))
	for _, entryPoint := range graph.EntryPoints {
		entrypointAddress[entryPoint.Name] = entryPoint.Address
	}
	for _, entry := range routes.L4Entries() {
		protocol := domain.RouterProtocolTCP
		backendProtocol := domain.NetworkProtocolTCP
		if entry.Kind == "udp" {
			protocol = domain.RouterProtocolUDP
			backendProtocol = domain.NetworkProtocolUDP
		}
		if !entry.Backend.Resolved() {
			return fmt.Errorf("app L4 route %q has no resolved backend", entry.RouterName)
		}
		// The declared bind must be the listener this router actually
		// attaches to: the runtime binds the entrypoint address, so a
		// narrower declaration must have been rejected earlier, never
		// silently widened here.
		address, ok := entrypointAddress[entry.Entrypoint]
		if !ok {
			return fmt.Errorf("app L4 route %q references entrypoint %q absent from the traffic graph", entry.RouterName, entry.Entrypoint)
		}
		listenerHost, listenerPort, err := domain.SplitListenerAddress(address)
		if err != nil {
			return fmt.Errorf("app L4 entrypoint %q has invalid listener address %q: %w", entry.Entrypoint, address, err)
		}
		if listenerPort != entry.BindPort || domain.CanonicalBindHost(listenerHost) != domain.CanonicalBindHost(entry.BindIP) {
			return fmt.Errorf(
				"app L4 route %q declares bind %s:%d but entrypoint %q binds %s",
				entry.RouterName, entry.BindIP, entry.BindPort, entry.Entrypoint, address,
			)
		}
		// service:<app>--<service>:<kind>-<port> satisfies the
		// service-ref grammar (no colon in the port name) and stays
		// unique per interface port. Historical builder services
		// never collide: they use route:/external_route:/
		// network_service: kinds or standalone names.
		serviceName := fmt.Sprintf("service:%s--%s:%s-%d", entry.App, entry.Service, entry.Kind, entry.Backend.ContainerPort)
		if _, ok := existing["router:"+entry.RouterName]; ok {
			return fmt.Errorf("app L4 router %q collides with a configured route", entry.RouterName)
		}
		if _, ok := existing["service:"+serviceName]; ok {
			return fmt.Errorf("app L4 service %q collides with a configured service", serviceName)
		}
		existing["router:"+entry.RouterName] = struct{}{}
		existing["service:"+serviceName] = struct{}{}
		graph.Routers = append(graph.Routers, domain.TrafficRouter{
			Name: entry.RouterName, EntryPoint: entry.Entrypoint,
			Protocol: protocol, Service: serviceName,
		})
		graph.Services = append(graph.Services, domain.TrafficService{
			Name: serviceName,
			Backends: []domain.TrafficBackend{{
				Name: entry.App + "--" + entry.Service,
				Host: entry.Backend.Host, Port: entry.Backend.Port, Protocol: backendProtocol,
			}},
		})
	}
	return graph.Validate()
}

// appHostDomainRoutes maps served hosts to builder route inputs.
func appHostDomainRoutes(hosts appHostRoutes) []domain.Route {
	if hosts == nil {
		return nil
	}
	appHosts := hosts.AppHosts()
	routes := make([]domain.Route, 0, len(appHosts))
	for _, h := range appHosts {
		routes = append(routes, domain.Route{Domain: h.Host})
	}
	return routes
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
