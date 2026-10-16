package traffic

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestNewEntryPointHTTPServerUsesConfiguredTimeouts(t *testing.T) {
	server := newEntryPointHTTPServer(http.NotFoundHandler(), domain.HTTPOptions{
		ReadHeaderTimeout:   3 * time.Second,
		IdleTimeout:         7 * time.Second,
		TransferIdleTimeout: 9 * time.Second,
	})

	assert.Equal(t, 3*time.Second, server.ReadHeaderTimeout)
	assert.Equal(t, 7*time.Second, server.IdleTimeout)
	assert.Zero(t, server.ReadTimeout, "whole-request read timeout must be replaced by the inactivity deadline")
	assert.Zero(t, server.WriteTimeout, "whole-response write timeout must be replaced by the inactivity deadline")
}

func TestManagerHTTPOptionsDefaultsWithoutSnapshot(t *testing.T) {
	assert.Equal(t, domain.DefaultHTTPOptions(), NewManager().httpOptions())
}

func TestEntryPointServersReceiveConfiguredHTTPOptions(t *testing.T) {
	manager := NewManager()
	graph := smartTCPGraph(t, freeTCPAddress(t), nil, nil)
	graph.Options.HTTP = domain.HTTPOptions{ReadHeaderTimeout: 3 * time.Second, IdleTimeout: 7 * time.Second, TransferIdleTimeout: 9 * time.Second}
	manager.SetSmartTCPHTTPServer("edge", http.NotFoundHandler(), nil)
	manager.SetSmartTCPTLSServer("edge", http.NotFoundHandler(), testTLSConfig(t, "app.example.com"))
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	runtime := tcpRuntimeForTest(t, manager, "edge")
	require.NotNil(t, runtime)
	runtime.mu.Lock()
	plain, secure := runtime.smartHTTPServer, runtime.smartTLSServer
	runtime.mu.Unlock()
	for name, server := range map[string]*http.Server{"plain": plain, "tls": secure} {
		require.NotNil(t, server, name)
		assert.Equal(t, 3*time.Second, server.ReadHeaderTimeout, name)
		assert.Equal(t, 7*time.Second, server.IdleTimeout, name)
		assert.Zero(t, server.ReadTimeout, name)
		assert.Zero(t, server.WriteTimeout, name)
	}
}

func TestTLSMuxServerReceivesConfiguredHTTPOptions(t *testing.T) {
	manager := NewManager()
	graph := domain.TrafficGraph{
		Options:     domain.TrafficOptions{HTTP: domain.HTTPOptions{ReadHeaderTimeout: 4 * time.Second, IdleTimeout: 8 * time.Second, TransferIdleTimeout: time.Minute}},
		EntryPoints: []domain.EntryPoint{{Name: "websecure", Address: freeTCPAddress(t), Protocol: domain.EntryPointProtocolTLSMux}},
	}
	manager.SetTLSHTTPServer("websecure", http.NotFoundHandler(), testTLSConfig(t, "app.example.com"))
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	runtime := tcpRuntimeForTest(t, manager, "websecure")
	require.NotNil(t, runtime)
	runtime.mu.Lock()
	server := runtime.tlsHTTPServer
	runtime.mu.Unlock()
	require.NotNil(t, server)
	assert.Equal(t, 4*time.Second, server.ReadHeaderTimeout)
	assert.Equal(t, 8*time.Second, server.IdleTimeout)
	assert.Zero(t, server.ReadTimeout)
	assert.Zero(t, server.WriteTimeout)
}

func TestSmartTCPStreamingResponseOutlivesTransferIdleTimeout(t *testing.T) {
	const idle = 300 * time.Millisecond
	manager := NewManager()
	graph := smartTCPGraph(t, freeTCPAddress(t), nil, nil)
	graph.Options.HTTP = domain.HTTPOptions{TransferIdleTimeout: idle}
	manager.SetSmartTCPHTTPServer("edge", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		silent := r.URL.Path == "/silent"
		for i := 0; i < 12; i++ {
			if _, err := w.Write([]byte("x")); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			if silent {
				// Stay silent until the inactivity timeout cancels the request.
				<-r.Context().Done()
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}), nil)
	require.NoError(t, manager.Apply(context.Background(), &graph))
	defer shutdownManager(t, manager)

	get := func(path string) (int, error) {
		conn := dialTCP(t, graph.EntryPoints[0].Address)
		defer conn.Close()
		_, err := conn.Write([]byte("GET " + path + " HTTP/1.1\r\nHost: app.example.com\r\nConnection: close\r\n\r\n"))
		require.NoError(t, err)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return len(body), err
	}

	n, err := get("/active")
	require.NoError(t, err)
	assert.Equal(t, 12, n, "active transfer longer than the idle timeout must complete")

	n, err = get("/silent")
	require.Error(t, err, "silent transfer must be cut")
	assert.Less(t, n, 12)
}
