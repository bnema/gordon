package container

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- TCP Probe Tests ---

func TestTCPProbe_Success(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	// Accept connections in background so the probe succeeds
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	ctx := testContext()
	err = tcpProbe(ctx, ln.Addr().String(), 5*time.Second)
	assert.NoError(t, err)
}

func TestTCPProbe_Timeout(t *testing.T) {
	// Use a port that nothing listens on (port 1 is privileged, won't have a listener)
	ctx := testContext()
	err := tcpProbe(ctx, "127.0.0.1:1", 200*time.Millisecond)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TCP probe timeout")
}

func TestTCPProbe_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(testContext())
	cancel()
	err := tcpProbe(ctx, "127.0.0.1:1", 5*time.Second)
	assert.Error(t, err)
}

func TestTCPProbe_DelayedListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	// Close initially so probe fails at first
	addr := ln.Addr().String()
	ln.Close()

	// Re-open after 1 second
	go func() {
		time.Sleep(time.Second)
		newLn, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		defer newLn.Close()
		for {
			conn, err := newLn.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	ctx := testContext()
	err = tcpProbe(ctx, addr, 5*time.Second)
	assert.NoError(t, err)
}

// --- HTTP Probe Tests ---

func TestHTTPProbe_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	ctx := testContext()
	err := httpProbe(ctx, srv.URL+"/healthz", 5*time.Second)
	assert.NoError(t, err)
}

func TestHTTPProbe_ServerError_ThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	ctx := testContext()
	err := httpProbe(ctx, srv.URL+"/healthz", 10*time.Second)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&calls), int32(3))
}

func TestHTTPProbe_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()

	ctx := testContext()
	err := httpProbe(ctx, srv.URL+"/healthz", 500*time.Millisecond)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP probe timeout")
}

func TestHTTPProbe_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(testContext())
	cancel()
	err := httpProbe(ctx, srv.URL+"/healthz", 5*time.Second)
	assert.Error(t, err)
}

func TestHTTPProbe_RedirectIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(302)
	}))
	defer srv.Close()

	ctx := testContext()
	// Disable follow redirects for the test — the probe should treat 3xx as success
	err := httpProbe(ctx, srv.URL+"/healthz", 5*time.Second)
	assert.NoError(t, err)
}
