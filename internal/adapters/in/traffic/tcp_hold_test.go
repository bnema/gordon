package traffic

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestTCPHoldRelease(t *testing.T) {
	for _, smart := range []bool{false, true} {
		name := "plain"
		if smart {
			name = "smart raw fallback"
		}
		t.Run(name, func(t *testing.T) {
			backend := startTCPEchoServer(t, 0)
			graph := tcpGraph(t, freeTCPAddress(t), backend.address)
			if smart {
				graph = smartTCPGraph(t, freeTCPAddress(t), nil, &smartTCPRoute{backendAddress: backend.address, trustedCIDRs: []string{"127.0.0.0/8"}})
			}
			resolved := cloneTrafficGraph(graph)
			serviceIndex := len(graph.Services) - 1
			graph.Services[serviceIndex].Backends = nil
			graph.Services[serviceIndex].HoldUntil = time.Now().Add(5 * time.Second)
			manager := NewManager()
			require.NoError(t, manager.Apply(context.Background(), &graph))
			defer shutdownManager(t, manager)
			conn := dialTCP(t, graph.EntryPoints[0].Address)
			defer conn.Close()
			// Smart TCP must classify before entering the raw fallback wait.
			payload := "\u0000held"
			if smart {
				_, err := conn.Write([]byte(payload))
				require.NoError(t, err)
			}
			require.Eventually(t, func() bool { return manager.Status().Counters.TotalHeld == 1 }, time.Second, time.Millisecond)
			require.Equal(t, int64(1), manager.Status().Counters.ActiveTCPConnections)
			require.Equal(t, int64(0), manager.Status().Counters.TotalAccepted)
			// A still-held replacement wakes the loop without counting twice.
			require.NoError(t, manager.Apply(context.Background(), &graph))
			require.NoError(t, manager.Apply(context.Background(), &resolved))
			if smart {
				require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
				got := make([]byte, len(payload))
				_, err := io.ReadFull(conn, got)
				require.NoError(t, err)
				require.Equal(t, payload, string(got))
			} else {
				assertRoundTrip(t, conn, payload)
			}
			require.Equal(t, int64(1), manager.Status().Counters.TotalHeld)
			require.Equal(t, int64(1), manager.Status().EntryPoints[0].TotalHeld)
		})
	}
}

func TestTCPHoldEnds(t *testing.T) {
	for _, outcome := range []string{"deadline", "stop", "withdraw"} {
		t.Run(outcome, func(t *testing.T) {
			backend := startTCPEchoServer(t, 0)
			graph := tcpGraph(t, freeTCPAddress(t), backend.address)
			graph.Services[0].Backends = nil
			graph.Services[0].HoldUntil = time.Now().Add(250 * time.Millisecond)
			if outcome != "deadline" {
				graph.Services[0].HoldUntil = time.Now().Add(time.Minute)
			}
			manager := NewManager()
			require.NoError(t, manager.Apply(context.Background(), &graph))
			defer shutdownManager(t, manager)
			conn := dialTCP(t, graph.EntryPoints[0].Address)
			defer conn.Close()
			require.Eventually(t, func() bool { return manager.Status().Counters.TotalHeld == 1 }, time.Second, time.Millisecond)
			if outcome == "stop" {
				require.NoError(t, manager.Shutdown(context.Background()))
			}
			if outcome == "withdraw" {
				graph.Routers = nil
				graph.Services = nil
				require.NoError(t, manager.Apply(context.Background(), &graph))
			}
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
			_, err := conn.Read(make([]byte, 1))
			require.Error(t, err)
			if outcome != "stop" {
				require.Eventually(t, func() bool { return manager.Status().Counters.TotalRefused == 1 }, time.Second, time.Millisecond)
			}
		})
	}
}

func TestTCPMissingBackendRefusesWithoutHold(t *testing.T) {
	graph := domain.TrafficGraph{EntryPoints: []domain.EntryPoint{{Name: "tcp", Address: freeTCPAddress(t), Protocol: domain.EntryPointProtocolTCP}}}
	manager := NewManager()
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)
	assertTCPRejected(t, manager, graph.EntryPoints[0].Address, 1)
	require.Zero(t, manager.Status().Counters.TotalHeld)
}

func TestTCPHoldDoesNotMissGraphChange(t *testing.T) {
	manager := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := &entryPointRuntime{manager: manager, ctx: ctx}
	calls := 0
	backend, ok := runtime.awaitTCPBackend(func() (domain.TrafficBackend, time.Time, bool) {
		calls++
		if calls == 1 {
			require.NoError(t, manager.Apply(ctx, &domain.TrafficGraph{}))
			return domain.TrafficBackend{}, time.Now().Add(time.Second), true
		}
		return domain.TrafficBackend{Port: 1234}, time.Time{}, true
	})
	require.True(t, ok)
	require.Equal(t, 1234, backend.Port)
	require.Equal(t, int64(1), runtime.counters.totalHeld.Load())
}
