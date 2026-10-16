package middleware

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testIdle = 300 * time.Millisecond

func newIdleServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(TransferIdleTimeout(testIdle)(handler))
	srv.Config.ReadHeaderTimeout = 5 * time.Second
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func TestTransferIdleTimeout_SlowActiveDownloadCompletes(t *testing.T) {
	const chunks = 12 // 12 x 50ms = 600ms, twice the idle timeout
	srv := newIdleServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for range chunks {
			_, _ = w.Write([]byte("x"))
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))

	start := time.Now()
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Len(t, body, chunks)
	assert.Greater(t, time.Since(start), testIdle, "transfer must outlast the idle timeout")
}

func TestTransferIdleTimeout_SilentResponseIsCut(t *testing.T) {
	srv := newIdleServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("start"))
		w.(http.Flusher).Flush()
		time.Sleep(3 * testIdle)
		_, _ = w.Write([]byte("late"))
		w.(http.Flusher).Flush()
	}))

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.Error(t, err, "connection must be cut after the idle timeout")
	assert.Equal(t, "start", string(body))
}

func TestTransferIdleTimeout_SilentHandlerBeforeFirstByteIsCut(t *testing.T) {
	srv := newIdleServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * testIdle)
		_, _ = w.Write([]byte("late"))
	}))

	resp, err := http.Get(srv.URL)
	if err == nil {
		defer resp.Body.Close()
		_, err = io.ReadAll(resp.Body)
	}
	require.Error(t, err)
}

func TestTransferIdleTimeout_SlowActiveUploadCompletes(t *testing.T) {
	const chunks = 10 // 10 x 60ms = 600ms
	srv := newIdleServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(strings.Repeat("o", int(n))))
	}))

	pr, pw := io.Pipe()
	go func() {
		for range chunks {
			_, _ = pw.Write([]byte("u"))
			time.Sleep(60 * time.Millisecond)
		}
		_ = pw.Close()
	}()
	resp, err := http.Post(srv.URL, "application/octet-stream", pr)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Len(t, body, chunks)
}

func TestTransferIdleTimeout_StalledUploadIsCut(t *testing.T) {
	var readErr atomic.Value
	done := make(chan struct{})
	srv := newIdleServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		defer close(done)
		_, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			readErr.Store(err)
		}
	}))

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Write([]byte("POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\nabc"))
	require.NoError(t, err)

	select {
	case <-done:
	case <-time.After(5 * testIdle):
		t.Fatal("stalled upload was not cut")
	}
	assert.NotNil(t, readErr.Load(), "body read must fail after the idle timeout")
}

func TestTransferIdleTimeout_PreservesFlusherAndUnwrap(t *testing.T) {
	var isFlusher, unwrapped bool
	h := TransferIdleTimeout(time.Second)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, isFlusher = w.(http.Flusher)
		_, unwrapped = w.(interface{ Unwrap() http.ResponseWriter })
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	assert.True(t, isFlusher)
	assert.True(t, unwrapped)
}

func TestTransferIdleTimeout_HijackStillWorks(t *testing.T) {
	srv := newIdleServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijacker", http.StatusInternalServerError)
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n")
		_ = rw.Flush()
		// Stay open longer than the idle timeout: hijacked streams are exempt.
		time.Sleep(2 * testIdle)
		_, _ = rw.WriteString("hello")
		_ = rw.Flush()
	}))

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n"))
	require.NoError(t, err)

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	buf := make([]byte, 5)
	_, err = io.ReadFull(br, buf)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(buf))
}

func TestTransferIdleTimeout_ReverseProxyFlushesStream(t *testing.T) {
	// Flush propagation through a wrapped writer, as used by httputil.ReverseProxy.
	srv := newIdleServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rc := http.NewResponseController(w)
		for range 8 {
			_, _ = w.Write([]byte("."))
			if err := rc.Flush(); err != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}))
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Len(t, body, 8)
}

func TestTransferIdleTimeout_DisabledWhenNonPositive(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	h := TransferIdleTimeout(0)(inner)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	assert.True(t, called)
}

func TestTransferIdleTimeout_HTTP2(t *testing.T) {
	const chunks = 12
	srv := httptest.NewUnstartedServer(TransferIdleTimeout(testIdle)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for range chunks {
			_, _ = w.Write([]byte("x"))
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
	})))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	client := srv.Client()
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 2, resp.ProtoMajor)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Len(t, body, chunks)
}
