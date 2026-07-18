package traffic

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

type backendResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type backendDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

// newSplitManager creates a manager for the split edge. It resolves backend
// aliases for every new backend connection and dials only a validated literal.
// Keeping the resolver and dialer injectable makes the security boundary
// deterministic to test.
func newSplitManager(resolver backendResolver, dialer backendDialer) *Manager {
	manager := newManager()
	manager.splitDialing = true
	if resolver != nil {
		manager.resolver = resolver
	}
	if dialer != nil {
		manager.dialer = dialer
	}
	return manager
}

func (m *Manager) dialBackend(ctx context.Context, network string, backend domain.TrafficBackend) (net.Conn, error) {
	address, err := m.backendDialAddress(ctx, backend)
	if err != nil {
		return nil, err
	}
	conn, err := m.dialer.DialContext(ctx, network, address)
	if err != nil {
		// Do not surface a resolved address: only the logical backend name is
		// safe to report outside this dial boundary.
		return nil, fmt.Errorf("dial backend %q", backend.Name)
	}
	return conn, nil
}

func (m *Manager) backendDialAddress(ctx context.Context, backend domain.TrafficBackend) (string, error) {
	host := strings.TrimSpace(backend.Host)
	if !m.splitDialing {
		return net.JoinHostPort(host, strconv.Itoa(backend.Port)), nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if err := domain.ValidateSplitTrafficIP(ip); err != nil {
			return "", fmt.Errorf("backend %q is not split reachable", backend.Name)
		}
		return net.JoinHostPort(ip.String(), strconv.Itoa(backend.Port)), nil
	}
	addresses, err := m.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return "", fmt.Errorf("resolve backend %q", backend.Name)
	}
	for _, address := range addresses {
		if !address.IsValid() || domain.ValidateSplitTrafficIP(address.AsSlice()) != nil {
			return "", fmt.Errorf("backend %q is not split reachable", backend.Name)
		}
	}
	return net.JoinHostPort(addresses[0].String(), strconv.Itoa(backend.Port)), nil
}
