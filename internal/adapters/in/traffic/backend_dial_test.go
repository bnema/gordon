package traffic

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

type staticBackendResolver struct {
	addresses []netip.Addr
	calls     int
}

func (r *staticBackendResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.calls++
	return r.addresses, nil
}

type recordingBackendDialer struct {
	mu        sync.Mutex
	addresses []string
}

func (d *recordingBackendDialer) DialContext(_ context.Context, _ string, address string) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addresses = append(d.addresses, address)
	client, backend := net.Pipe()
	_ = backend.Close()
	return client, nil
}

func (d *recordingBackendDialer) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.addresses)
}

func TestSplitBackendDialRejectsForbiddenDNSResultsWithoutDialing(t *testing.T) {
	for _, test := range []struct {
		name      string
		addresses []netip.Addr
	}{
		{name: "loopback", addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}},
		{name: "link local", addresses: []netip.Addr{netip.MustParseAddr("169.254.1.1")}},
		{name: "mixed safe and forbidden", addresses: []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("::1")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := &staticBackendResolver{addresses: test.addresses}
			dialer := &recordingBackendDialer{}
			manager := newSplitManager(resolver, dialer)

			_, err := manager.dialBackend(context.Background(), "tcp", domain.TrafficBackend{Name: "service:api:tcp", Host: "backend.example", Port: 443})

			require.Error(t, err)
			assert.Equal(t, 1, resolver.calls)
			assert.Zero(t, dialer.calls())
		})
	}
}

func TestSplitBackendDialUsesValidatedIPLiteralForTCPAndUDP(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			resolver := &staticBackendResolver{addresses: []netip.Addr{netip.MustParseAddr("10.20.30.40")}}
			dialer := &recordingBackendDialer{}
			manager := newSplitManager(resolver, dialer)

			conn, err := manager.dialBackend(context.Background(), network, domain.TrafficBackend{Name: "service:api:" + network, Host: "backend.example", Port: 443})

			require.NoError(t, err)
			require.NoError(t, conn.Close())
			assert.Equal(t, 1, resolver.calls)
			assert.Equal(t, []string{"10.20.30.40:443"}, dialer.addresses)
		})
	}
}

func TestSplitBackendDialValidatesLiteralWithoutResolving(t *testing.T) {
	resolver := &staticBackendResolver{addresses: []netip.Addr{netip.MustParseAddr("10.20.30.40")}}
	dialer := &recordingBackendDialer{}
	manager := newSplitManager(resolver, dialer)

	_, err := manager.dialBackend(context.Background(), "tcp", domain.TrafficBackend{Name: "service:api:tcp", Host: "127.0.0.1", Port: 443})

	require.Error(t, err)
	assert.Zero(t, resolver.calls)
	assert.Zero(t, dialer.calls())
}

func TestSplitBackendDialUsesSafeLiteralWithoutResolving(t *testing.T) {
	resolver := &staticBackendResolver{}
	dialer := &recordingBackendDialer{}
	manager := newSplitManager(resolver, dialer)

	conn, err := manager.dialBackend(context.Background(), "tcp", domain.TrafficBackend{Name: "service:api:tcp", Host: "10.20.30.40", Port: 443})

	require.NoError(t, err)
	require.NoError(t, conn.Close())
	assert.Zero(t, resolver.calls)
	assert.Equal(t, []string{"10.20.30.40:443"}, dialer.addresses)
}
