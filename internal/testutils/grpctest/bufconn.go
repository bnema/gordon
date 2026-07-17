// Package grpctest provides in-memory gRPC test fixtures without application wiring.
package grpctest

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const bufferSize = 1024 * 1024

// RegisterFunc registers test services with an in-memory gRPC server.
type RegisterFunc func(grpc.ServiceRegistrar)

// Harness owns an in-memory gRPC server and all connections dialed through it.
type Harness struct {
	listener *bufconn.Listener
	server   *grpc.Server

	mu     sync.Mutex
	conns  []*grpc.ClientConn
	closed bool
}

// NewHarness starts an in-memory gRPC server and registers cleanup with t.
func NewHarness(t testing.TB, register RegisterFunc, serverOptions ...grpc.ServerOption) *Harness {
	t.Helper()

	harness := &Harness{
		listener: bufconn.Listen(bufferSize),
		server:   grpc.NewServer(serverOptions...),
	}
	register(harness.server)
	go func() {
		_ = harness.server.Serve(harness.listener)
	}()
	t.Cleanup(harness.Close)

	return harness
}

// Conn returns an unauthenticated connection to the in-memory server.
func (h *Harness) Conn(t testing.TB, dialOptions ...grpc.DialOption) *grpc.ClientConn {
	t.Helper()
	return h.dial(t, dialOptions...)
}

// AuthenticatedConn returns a connection that sends token as bearer metadata.
func (h *Harness) AuthenticatedConn(t testing.TB, token string, dialOptions ...grpc.DialOption) *grpc.ClientConn {
	t.Helper()
	dialOptions = append(dialOptions, grpc.WithPerRPCCredentials(localBearerCredentials{token: token}))
	return h.dial(t, dialOptions...)
}

func (h *Harness) dial(t testing.TB, dialOptions ...grpc.DialOption) *grpc.ClientConn {
	t.Helper()

	options := []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return h.listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
	options = append(options, dialOptions...)
	conn, err := grpc.NewClient("passthrough:///bufnet", options...)
	require.NoError(t, err)

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = conn.Close()
		t.Fatal("grpctest: connection requested after harness closed")
	}
	h.conns = append(h.conns, conn)
	h.mu.Unlock()

	return conn
}

// Close deterministically closes client connections, the server, and its listener.
// It is safe to call more than once.
func (h *Harness) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	conns := h.conns
	h.conns = nil
	h.mu.Unlock()

	for _, conn := range conns {
		_ = conn.Close()
	}
	h.server.Stop()
	_ = h.listener.Close()
}
