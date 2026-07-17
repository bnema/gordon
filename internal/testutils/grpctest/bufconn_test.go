package grpctest_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/bnema/gordon/internal/testutils/grpctest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestHarnessSupportsRegistrationAndServerInterceptors(t *testing.T) {
	var unaryCalls atomic.Int32
	var streamCalls atomic.Int32
	harness := grpctest.NewHarness(t, func(registrar grpc.ServiceRegistrar) {
		healthv1.RegisterHealthServer(registrar, health.NewServer())
	},
		grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			unaryCalls.Add(1)
			return handler(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			streamCalls.Add(1)
			return handler(srv, stream)
		}),
	)
	client := healthv1.NewHealthClient(harness.Conn(t))

	_, err := client.Check(context.Background(), &healthv1.HealthCheckRequest{})
	require.NoError(t, err)
	stream, err := client.Watch(context.Background(), &healthv1.HealthCheckRequest{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, int32(1), unaryCalls.Load())
	assert.Equal(t, int32(1), streamCalls.Load())
}

func TestHarnessProvidesAuthenticatedAndUnauthenticatedConnections(t *testing.T) {
	seenTokens := make(chan []string, 2)
	harness := grpctest.NewHarness(t, func(registrar grpc.ServiceRegistrar) {
		healthv1.RegisterHealthServer(registrar, health.NewServer())
	}, grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		seenTokens <- md.Get("authorization")
		return handler(ctx, req)
	}))

	_, err := healthv1.NewHealthClient(harness.Conn(t)).Check(context.Background(), &healthv1.HealthCheckRequest{})
	require.NoError(t, err)
	_, err = healthv1.NewHealthClient(harness.AuthenticatedConn(t, grpctest.LocalComponentToken)).Check(context.Background(), &healthv1.HealthCheckRequest{})
	require.NoError(t, err)

	assert.Empty(t, <-seenTokens)
	assert.Equal(t, []string{"Bearer " + grpctest.LocalComponentToken}, <-seenTokens)
}

func TestHarnessCloseStopsServerAndIsIdempotent(t *testing.T) {
	harness := grpctest.NewHarness(t, func(registrar grpc.ServiceRegistrar) {
		healthv1.RegisterHealthServer(registrar, health.NewServer())
	})
	client := healthv1.NewHealthClient(harness.Conn(t))
	_, err := client.Check(context.Background(), &healthv1.HealthCheckRequest{})
	require.NoError(t, err)

	harness.Close()
	harness.Close()

	_, err = client.Check(context.Background(), &healthv1.HealthCheckRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Canceled, status.Code(err))
}
