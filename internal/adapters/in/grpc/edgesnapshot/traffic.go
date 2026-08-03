package edgesnapshot

import (
	"fmt"
	"math"
	"time"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
	"github.com/bnema/gordon/internal/domain"
)

// TrafficGraphSnapshotToProto converts only the complete sanitized graph model.
func TrafficGraphSnapshotToProto(snapshot domain.TrafficGraphSnapshot) (*edgev1.WatchTrafficGraphsResponse, error) {
	if err := snapshot.ValidateSplitReachability(); err != nil {
		return nil, fmt.Errorf("validate traffic graph snapshot: %w", err)
	}
	graph := snapshot.Graph
	message := &edgev1.WatchTrafficGraphsResponse{
		Generation: uint64(snapshot.Generation),
		Options: &edgev1.TrafficOptions{Tcp: &edgev1.TrafficTCPOptions{
			DialTimeoutNanos: int64(graph.Options.TCP.DialTimeout), IdleTimeoutNanos: int64(graph.Options.TCP.IdleTimeout),
			DrainTimeoutNanos: int64(graph.Options.TCP.DrainTimeout), MaxConnections: int64(graph.Options.TCP.MaxConnections),
		}, Udp: &edgev1.TrafficUDPOptions{
			IdleTimeoutNanos: int64(graph.Options.UDP.IdleTimeout), DrainTimeoutNanos: int64(graph.Options.UDP.DrainTimeout), MaxSessions: int64(graph.Options.UDP.MaxSessions),
		}},
		EntryPoints: make([]*edgev1.TrafficEntryPoint, 0, len(graph.EntryPoints)),
		Routers:     make([]*edgev1.TrafficRouter, 0, len(graph.Routers)),
		Services:    make([]*edgev1.TrafficService, 0, len(graph.Services)),
	}
	if graph.Options.TCP.MaxConnections < 0 || graph.Options.UDP.MaxSessions < 0 {
		return nil, fmt.Errorf("negative traffic limit")
	}
	for _, entry := range graph.EntryPoints {
		message.EntryPoints = append(message.EntryPoints, &edgev1.TrafficEntryPoint{Name: entry.Name, Address: entry.Address, Protocol: string(entry.Protocol), TrustedCidrs: append([]string(nil), entry.TrustedCIDRs...), RawFallback: entry.RawFallback, RawFallbackTrustedCidrs: append([]string(nil), entry.RawFallbackTrustedCIDRs...), AllowPublicRawFallback: entry.AllowPublicRawFallback})
	}
	for _, router := range graph.Routers {
		message.Routers = append(message.Routers, &edgev1.TrafficRouter{Name: router.Name, EntryPoint: router.EntryPoint, Protocol: string(router.Protocol), Rule: &edgev1.TrafficRule{Host: router.Rule.Host, Sni: router.Rule.SNI}, Service: router.Service})
	}
	for _, service := range graph.Services {
		converted := &edgev1.TrafficService{Name: service.Name, Backends: make([]*edgev1.TrafficBackend, 0, len(service.Backends))}
		for _, backend := range service.Backends {
			if backend.Port < 0 || backend.Port > maxProtoInt32 {
				return nil, fmt.Errorf("backend port overflows transport")
			}
			converted.Backends = append(converted.Backends, &edgev1.TrafficBackend{Name: backend.Name, Host: backend.Host, Port: int32(backend.Port), Protocol: string(backend.Protocol)})
		}
		message.Services = append(message.Services, converted)
	}
	return message, nil
}

// TrafficGraphSnapshotFromProto validates every received graph field and its
// split reachability before exposing it to an edge.
func TrafficGraphSnapshotFromProto(message *edgev1.WatchTrafficGraphsResponse) (domain.TrafficGraphSnapshot, error) {
	if message == nil || message.Options == nil || message.Options.Tcp == nil || message.Options.Udp == nil {
		return domain.TrafficGraphSnapshot{}, fmt.Errorf("traffic graph snapshot options are required")
	}
	if message.Options.Tcp.MaxConnections > math.MaxInt || message.Options.Udp.MaxSessions > math.MaxInt || message.Options.Tcp.MaxConnections < 0 || message.Options.Udp.MaxSessions < 0 {
		return domain.TrafficGraphSnapshot{}, fmt.Errorf("traffic graph limit is invalid")
	}
	graph, err := trafficGraphFromProtoFields(message)
	if err != nil {
		return domain.TrafficGraphSnapshot{}, err
	}
	snapshot := domain.TrafficGraphSnapshot{Generation: domain.TrafficGraphGeneration(message.Generation), Graph: graph}
	if err := snapshot.ValidateSplitReachability(); err != nil {
		return domain.TrafficGraphSnapshot{}, fmt.Errorf("validate traffic graph snapshot: %w", err)
	}
	return snapshot, nil
}

func trafficGraphFromProtoFields(message *edgev1.WatchTrafficGraphsResponse) (domain.TrafficGraph, error) {
	graph := domain.TrafficGraph{Options: domain.TrafficOptions{TCP: domain.TCPOptions{DialTimeout: time.Duration(message.Options.Tcp.DialTimeoutNanos), IdleTimeout: time.Duration(message.Options.Tcp.IdleTimeoutNanos), DrainTimeout: time.Duration(message.Options.Tcp.DrainTimeoutNanos), MaxConnections: int(message.Options.Tcp.MaxConnections)}, UDP: domain.UDPOptions{IdleTimeout: time.Duration(message.Options.Udp.IdleTimeoutNanos), DrainTimeout: time.Duration(message.Options.Udp.DrainTimeoutNanos), MaxSessions: int(message.Options.Udp.MaxSessions)}}}
	var err error
	if graph.EntryPoints, err = trafficEntryPointsFromProto(message.EntryPoints); err != nil {
		return domain.TrafficGraph{}, err
	}
	if graph.Routers, err = trafficRoutersFromProto(message.Routers); err != nil {
		return domain.TrafficGraph{}, err
	}
	if graph.Services, err = trafficServicesFromProto(message.Services); err != nil {
		return domain.TrafficGraph{}, err
	}
	return graph, nil
}

func trafficEntryPointsFromProto(values []*edgev1.TrafficEntryPoint) ([]domain.EntryPoint, error) {
	converted := make([]domain.EntryPoint, 0, len(values))
	for index, entry := range values {
		if entry == nil {
			return nil, fmt.Errorf("entrypoint %d is required", index)
		}
		converted = append(converted, domain.EntryPoint{Name: entry.Name, Address: entry.Address, Protocol: domain.EntryPointProtocol(entry.Protocol), TrustedCIDRs: append([]string(nil), entry.TrustedCidrs...), RawFallback: entry.RawFallback, RawFallbackTrustedCIDRs: append([]string(nil), entry.RawFallbackTrustedCidrs...), AllowPublicRawFallback: entry.AllowPublicRawFallback})
	}
	return converted, nil
}

func trafficRoutersFromProto(values []*edgev1.TrafficRouter) ([]domain.TrafficRouter, error) {
	converted := make([]domain.TrafficRouter, 0, len(values))
	for index, router := range values {
		if router == nil || router.Rule == nil {
			return nil, fmt.Errorf("router %d is required", index)
		}
		converted = append(converted, domain.TrafficRouter{Name: router.Name, EntryPoint: router.EntryPoint, Protocol: domain.RouterProtocol(router.Protocol), Rule: domain.TrafficRule{Host: router.Rule.Host, SNI: router.Rule.Sni}, Service: router.Service})
	}
	return converted, nil
}

func trafficServicesFromProto(values []*edgev1.TrafficService) ([]domain.TrafficService, error) {
	converted := make([]domain.TrafficService, 0, len(values))
	for serviceIndex, service := range values {
		if service == nil {
			return nil, fmt.Errorf("service %d is required", serviceIndex)
		}
		result := domain.TrafficService{Name: service.Name, Backends: make([]domain.TrafficBackend, 0, len(service.Backends))}
		for backendIndex, backend := range service.Backends {
			if backend == nil || backend.Port < 0 {
				return nil, fmt.Errorf("service %d backend %d is invalid", serviceIndex, backendIndex)
			}
			result.Backends = append(result.Backends, domain.TrafficBackend{Name: backend.Name, Host: backend.Host, Port: int(backend.Port), Protocol: domain.NetworkProtocol(backend.Protocol)})
		}
		converted = append(converted, result)
	}
	return converted, nil
}
