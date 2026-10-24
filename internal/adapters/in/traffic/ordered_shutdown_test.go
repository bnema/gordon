package traffic

import (
	"bufio"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestManagerRefusesBindsOnceShutdownStarts(t *testing.T) {
	m := NewManager()
	// Hold the graph lock so Shutdown is in progress, not merely complete.
	m.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- m.Shutdown(context.Background()) }()
	assert.Eventually(t, m.stopped.Load, time.Second, time.Millisecond)
	for _, protocol := range []domain.EntryPointProtocol{domain.EntryPointProtocolTCP, domain.EntryPointProtocolUDP} {
		entry := domain.EntryPoint{Name: "late", Address: "127.0.0.1:0", Protocol: protocol}
		if protocol == domain.EntryPointProtocolTCP {
			_, err := m.bindTCPEntryPoint(context.Background(), entry)
			require.ErrorIs(t, err, domain.ErrTrafficManagerStopped)
		} else {
			_, err := m.bindUDPEntryPoint(context.Background(), entry)
			require.ErrorIs(t, err, domain.ErrTrafficManagerStopped)
		}
	}
	require.ErrorIs(t, m.Apply(context.Background(), &domain.TrafficGraph{}), domain.ErrTrafficManagerStopped)
	m.mu.Unlock()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("shutdown did not complete")
	}
}

func TestManagerShutdownDeadlineForceClosesRelay(t *testing.T) {
	backend := startTCPEchoServer(t, 0)
	graph := tcpGraph(t, freeTCPAddress(t), backend.address)
	m := NewManager()
	require.NoError(t, m.Apply(context.Background(), &graph))
	conn := dialTCP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	_, err := conn.Write([]byte("ready\n"))
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	line, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready\n", line)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	require.NoError(t, m.Shutdown(ctx))
	assert.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	assert.Less(t, time.Since(started), time.Second)
	_, err = conn.Read(make([]byte, 1))
	require.Error(t, err)
	var netErr net.Error
	assert.False(t, errors.As(err, &netErr) && netErr.Timeout(), "relay must close rather than hit client read deadline")
}

// Block at the retained-listener preparation log, after the initial admission
// check but before commit. No timing sleeps or production test hooks are needed.
func TestApplyRejectsRetainedUpdateDuringPreparation(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "stop"}[stop], func(t *testing.T) {
			m := NewManager()
			graph := tcpGraph(t, freeTCPAddress(t), "127.0.0.1:12345")
			require.NoError(t, m.Apply(context.Background(), &graph))
			defer shutdownManager(t, m)
			original := m.snapshot.Load()
			runtime := m.listeners["tcp"]
			next := cloneTrafficGraph(graph)
			next.EntryPoints[0].TrustedCIDRs = []string{"127.0.0.0/8"}
			prepared, release := make(chan struct{}), make(chan struct{})
			log := zerowrap.WithHook(zerowrap.New(zerowrap.Config{Level: "debug"}), zerolog.HookFunc(func(_ *zerolog.Event, _ zerolog.Level, msg string) {
				if msg == "reusing tcp traffic listener for same-address entrypoint update" {
					close(prepared)
					<-release
				}
			}))
			ctx, cancel := context.WithCancel(zerowrap.WithCtx(context.Background(), log))
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- m.Apply(ctx, &next) }()
			select {
			case <-prepared:
			case <-time.After(time.Second):
				t.Fatal("preparation not reached")
			}
			stopped := make(chan struct{})
			expected := context.Canceled
			if stop {
				expected = domain.ErrTrafficManagerStopped
				go func() { m.StopAccepting(); close(stopped) }()
				require.Eventually(t, m.stopped.Load, time.Second, time.Millisecond)
			} else {
				cancel()
				close(stopped)
			}
			close(release)
			select {
			case err := <-done:
				require.ErrorIs(t, err, expected)
			case <-time.After(time.Second):
				t.Fatal("apply did not return")
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("stop did not return")
			}
			assert.Same(t, original, m.snapshot.Load())
			assert.Empty(t, runtime.entryPointSnapshot().TrustedCIDRs)
		})
	}
}

func TestTerminalShutdownUsesCallerDeadlineInsteadOfGraphDrain(t *testing.T) {
	backend := startTCPEchoServer(t, 0)
	graph := tcpGraph(t, freeTCPAddress(t), backend.address)
	graph.Options.TCP.DrainTimeout = 10 * time.Millisecond
	m := NewManager()
	require.NoError(t, m.Apply(context.Background(), &graph))
	defer shutdownManager(t, m)
	conn := dialTCP(t, graph.EntryPoints[0].Address)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
	reader := bufio.NewReader(conn)
	echo := func() {
		_, err := conn.Write([]byte("alive\n"))
		require.NoError(t, err)
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, "alive\n", line)
	}
	echo() // Ensure the relay is active before shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Shutdown(ctx) }()
	require.Eventually(t, m.stopped.Load, time.Second, time.Millisecond)
	// Wait beyond the graph retirement budget, while retaining plenty of the
	// terminal budget. An echo proves the connection was not retired early.
	timer := time.NewTimer(3 * graph.Options.TCP.DrainTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		t.Fatalf("shutdown ended before caller deadline: %v", err)
	case <-timer.C:
	}
	echo()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish")
	}
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	_, err := reader.ReadByte()
	require.Error(t, err)
	var netErr net.Error
	assert.False(t, errors.As(err, &netErr) && netErr.Timeout(), "relay must be closed at the shutdown deadline")
}
