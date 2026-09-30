package deployment

import (
	"context"
	"fmt"
	"sort"

	"github.com/bnema/gordon/internal/domain"
)

// backendPorts collects every interface container port for loopback
// publication: public HTTP + TCP interfaces on tcp plus UDP interfaces on
// udp, plus an explicit TCP readiness port. Internal HTTP ports are
// excluded: they are reached only over the private network and must keep
// no host binding. Each publish is on 127.0.0.1 ephemeral (never public).
// Deduplicated by (protocol, container port).
func backendPorts(spec domain.AppService) []domain.ContainerBackendPort {
	seen := map[domain.ContainerBackendPort]struct{}{}
	var ports []domain.ContainerBackendPort
	add := func(port int, protocol domain.NetworkProtocol) {
		if port <= 0 {
			return
		}
		key := domain.ContainerBackendPort{ContainerPort: port, Protocol: protocol}
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			ports = append(ports, key)
		}
	}
	for _, h := range spec.HTTP {
		if !h.IsPublic() {
			continue
		}
		add(h.Port, domain.NetworkProtocolTCP)
	}
	for _, t := range spec.TCP {
		add(t.Port, domain.NetworkProtocolTCP)
	}
	for _, u := range spec.UDP {
		add(u.Port, domain.NetworkProtocolUDP)
	}
	// Readiness probes are TCP-only (validation rejects UDP-only
	// services with tcp/http readiness); the readiness port matches a
	// declared TCP container port. An internal-only port is never
	// published, so readiness metadata cannot create a host binding for
	// it.
	if spec.Readiness.Port > 0 && !spec.InternallyOnlyPort(spec.Readiness.Port) {
		add(spec.Readiness.Port, domain.NetworkProtocolTCP)
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Protocol != ports[j].Protocol {
			return ports[i].Protocol < ports[j].Protocol
		}
		return ports[i].ContainerPort < ports[j].ContainerPort
	})
	return ports
}

// backendPublishes maps backend ports to 127.0.0.1 ephemeral publishes.
func backendPublishes(spec domain.AppService) []domain.ContainerPortPublish {
	ports := backendPorts(spec)
	publishes := make([]domain.ContainerPortPublish, 0, len(ports))
	for _, port := range ports {
		publishes = append(publishes, domain.ContainerPortPublish{
			HostIP:        "127.0.0.1",
			HostPort:      0,
			ContainerPort: port.ContainerPort,
			Protocol:      port.Protocol,
		})
	}
	return publishes
}

// readBackendBinds resolves each published container port to its
// 127.0.0.1 host port and registers the Gordon-generated claims
// (owner gordon-backend) in the global checkpoint. Missing binds fail:
// an unbound backend can serve neither readiness nor proxy traffic.
// A registration conflict removes the CANDIDATE container and fails —
// callers must only pass freshly created candidates here, never an
// existing ACTIVE container (see inspectBackendBinds).
func (s *Service) readBackendBinds(ctx context.Context, app, service, containerID string, ports []domain.ContainerBackendPort) (map[int]int, map[int]int, error) {
	if len(ports) == 0 {
		return map[int]int{}, map[int]int{}, nil
	}
	observed, err := s.deps.Runtime.GetContainerBackendBinds(ctx, containerID, ports)
	if err != nil {
		return nil, nil, fmt.Errorf("deployment: backend binds: %w", err)
	}
	binds, udpBinds := splitBackendBinds(observed)
	claims := backendClaims(app, service, containerID, observed)
	if len(claims) == 0 {
		return binds, udpBinds, nil
	}
	if err := s.deps.State.RegisterBackendBinds(ctx, claims); err != nil {
		s.retireCandidate(ctx, app, service, containerID)
		return nil, nil, err
	}
	return binds, udpBinds, nil
}

// inspectBackendBinds re-inspects the loopback publishes of an EXISTING
// container and registers the Gordon-generated claims. Unlike
// readBackendBinds it never stops or removes the container: a
// registration conflict fails without touching the ACTIVE workload.
// Missing binds fail: an unbound backend must not stay routable.
func (s *Service) inspectBackendBinds(ctx context.Context, app, service, containerID string, ports []domain.ContainerBackendPort) (map[int]int, map[int]int, error) {
	if len(ports) == 0 {
		return map[int]int{}, map[int]int{}, nil
	}
	observed, err := s.deps.Runtime.GetContainerBackendBinds(ctx, containerID, ports)
	if err != nil {
		return nil, nil, fmt.Errorf("deployment: backend binds: %w", err)
	}
	binds, udpBinds := splitBackendBinds(observed)
	claims := backendClaims(app, service, containerID, observed)
	if len(claims) == 0 {
		return binds, udpBinds, nil
	}
	if err := s.deps.State.RegisterBackendBinds(ctx, claims); err != nil {
		return nil, nil, err
	}
	return binds, udpBinds, nil
}

// splitBackendBinds partitions observed binds by protocol. TCP feeds
// readiness and HTTP/TCP proxying; UDP feeds UDP relaying.
func splitBackendBinds(observed []domain.ContainerBackendBind) (map[int]int, map[int]int) {
	binds := map[int]int{}
	udpBinds := map[int]int{}
	for _, bind := range observed {
		if bind.Protocol == domain.NetworkProtocolUDP {
			udpBinds[bind.ContainerPort] = bind.HostPort
		} else {
			binds[bind.ContainerPort] = bind.HostPort
		}
	}
	return binds, udpBinds
}

// backendClaims maps observed binds to Gordon-generated loopback claims
// with the exact protocol (tcp/udp) for the global checkpoint.
func backendClaims(app, service, containerID string, observed []domain.ContainerBackendBind) []domain.AppListenerReservation {
	claims := make([]domain.AppListenerReservation, 0, len(observed))
	for _, bind := range observed {
		claims = append(claims, domain.AppListenerReservation{
			Proto:       string(bind.Protocol),
			IP:          "127.0.0.1",
			Port:        bind.HostPort,
			Service:     service,
			App:         app,
			Owner:       domain.OwnerGordonBackend,
			ContainerID: containerID,
		})
	}
	return claims
}
