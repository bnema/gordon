package deployment

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serveTCP accepts connections on loopback and hands each to handle.
func serveTCP(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(conn)
		}
	}()
	return ln.Addr().String()
}

const testSettle = 100 * time.Millisecond

func TestTCPProbeOnce_RelayAcceptThenCloseIsNotReady(t *testing.T) {
	// A host port relay with no listener behind it accepts and closes at once.
	addr := serveTCP(t, func(c net.Conn) { _ = c.Close() })

	err := tcpProbeOnce(context.Background(), addr, testSettle)

	require.ErrorIs(t, err, errTCPProbeClosed)
}

func TestTCPProbeOnce_RelayResetIsNotReady(t *testing.T) {
	addr := serveTCP(t, func(c net.Conn) {
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.SetLinger(0) // close with RST
		}
		_ = c.Close()
	})

	err := tcpProbeOnce(context.Background(), addr, testSettle)

	require.Error(t, err)
}

func TestTCPProbeOnce_SilentListenerIsReady(t *testing.T) {
	// A workload that never speaks first (Postgres, Redis) keeps the connection open.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	addr := serveTCP(t, func(c net.Conn) {
		<-release
		_ = c.Close()
	})

	start := time.Now()
	err := tcpProbeOnce(context.Background(), addr, testSettle)

	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), testSettle)
}

func TestTCPProbeOnce_BannerIsReadyImmediately(t *testing.T) {
	// A workload that speaks first (SSH, SMTP) is ready on its first byte.
	addr := serveTCP(t, func(c net.Conn) {
		_, _ = c.Write([]byte("SSH-2.0-test\r\n"))
		_ = c.Close()
	})

	start := time.Now()
	err := tcpProbeOnce(context.Background(), addr, 5*time.Second)

	require.NoError(t, err)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestTCPProbeOnce_NoListenerFails(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	require.Error(t, tcpProbeOnce(context.Background(), addr, testSettle))
}

func TestTCPProbeOnce_ContextCancelStopsTheSettleWait(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	addr := serveTCP(t, func(c net.Conn) {
		<-release
		_ = c.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := tcpProbeOnce(ctx, addr, 10*time.Second)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second)
}
