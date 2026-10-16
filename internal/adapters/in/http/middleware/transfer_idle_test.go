package middleware

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
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

// pipeListener serves connections created with dial over net.Pipe, which has no
// kernel buffering: a write completes only when the peer reads it.
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

func (l *pipeListener) dial() net.Conn {
	client, server := net.Pipe()
	l.conns <- server
	return client
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// slowReadConn reads at most 4 KiB every pause, like a congested client.
type slowReadConn struct {
	net.Conn
	pause time.Duration
}

func (c *slowReadConn) Read(p []byte) (int, error) {
	time.Sleep(c.pause)
	return c.Conn.Read(p[:min(len(p), 4096)])
}

func servePipe(t *testing.T, handler http.Handler) *pipeListener {
	t.Helper()
	ln := newPipeListener()
	srv := &http.Server{Handler: TransferIdleTimeout(testIdle)(handler), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln
}

func TestTransferIdleTimeout_BackpressuredButReadingClientIsNotCut(t *testing.T) {
	const size = 64 * 1024
	ln := servePipe(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("d", size)))
	}))
	client := &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return &slowReadConn{Conn: ln.dial(), pause: 25 * time.Millisecond}, nil
		},
	}}

	start := time.Now()
	resp, err := client.Get("http://pipe/")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "a client that keeps reading must not be cut")
	assert.Len(t, body, size)
	assert.Greater(t, time.Since(start), testIdle, "the transfer must outlast the idle timeout")
}

func TestTransferIdleTimeout_NonReadingClientUnblocksWrite(t *testing.T) {
	writeErr := make(chan error, 1)
	ln := servePipe(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte(strings.Repeat("d", 1<<20)))
		writeErr <- err
	}))
	conn := ln.dial()
	defer conn.Close()
	_, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	require.NoError(t, err)

	select {
	case err := <-writeErr:
		require.Error(t, err, "write to a client that never reads must fail")
	case <-time.After(10 * testIdle):
		t.Fatal("blocked write was not unblocked by the inactivity timeout")
	}
}

func TestTransferIdleTimeout_SilentHandlerContextCanceledAndConnectionClosed(t *testing.T) {
	ctxDone := make(chan struct{})
	srv := newIdleServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("start"))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // a ReverseProxy would abort its upstream request here
		close(ctxDone)
	}))

	client := &http.Client{Timeout: 10 * time.Second}
	begin := time.Now()
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.Error(t, err, "the client connection must be closed, not completed cleanly")
	assert.Equal(t, "start", string(body))
	assert.Less(t, time.Since(begin), 5*time.Second)

	select {
	case <-ctxDone:
	case <-time.After(5 * time.Second):
		t.Fatal("request context was not canceled")
	}
}

func TestTransferIdleTimeout_ProgressKeepsContextAlive(t *testing.T) {
	var canceled atomic.Bool
	srv := newIdleServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for range 12 {
			_, _ = w.Write([]byte("x"))
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				canceled.Store(true)
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}))
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Len(t, body, 12)
	assert.False(t, canceled.Load())
}

func TestTransferIdleTimeout_BodyUseAfterHandlerReturnedIsSafe(t *testing.T) {
	var body io.ReadCloser
	h := TransferIdleTimeout(time.Second)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body = r.Body
	}))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("payload"))
	h.ServeHTTP(httptest.NewRecorder(), req)

	// Like http.Transport closing the outbound body after the handler returned.
	require.NoError(t, body.Close())
	require.NoError(t, body.Close(), "Close must be idempotent")
	_, _ = body.Read(make([]byte, 4))
}

// TestTransferIdleTimeout_HTTP2ReverseProxyEarlyResponseDuringUpload covers the
// transport closing the outbound request body after the handler returned, when
// the upstream answers early (413) while the client is still uploading. Run
// with -race.
func TestTransferIdleTimeout_HTTP2ReverseProxyEarlyResponseDuringUpload(t *testing.T) {
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test upstream
		ForceAttemptHTTP2: true,
	}
	front := httptest.NewUnstartedServer(TransferIdleTimeout(testIdle)(proxy))
	front.EnableHTTP2 = true
	front.StartTLS()
	defer front.Close()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				uploadUntilEarlyResponse(t, front)
			}
		}()
	}
	wg.Wait()
}

func uploadUntilEarlyResponse(t *testing.T, front *httptest.Server) {
	t.Helper()
	pr, pw := io.Pipe()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		chunk := []byte(strings.Repeat("u", 4096))
		for range 200 {
			if _, err := pw.Write(chunk); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
		_ = pw.Close()
	}()

	defer func() {
		_ = pr.CloseWithError(io.ErrClosedPipe)
		<-writerDone
	}()

	req, err := http.NewRequest(http.MethodPost, front.URL, pr)
	if !assert.NoError(t, err) {
		return
	}
	resp, err := front.Client().Do(req)
	if !assert.NoError(t, err, "the early 413 must reach the client") {
		return
	}
	defer resp.Body.Close()
	assert.Equal(t, 2, resp.ProtoMajor)
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	_, _ = io.Copy(io.Discard, resp.Body)
}

// TestTransferIdleTimeout_HTTP2BodyUsedAfterHandlerReturned uses the request
// body after net/http's HTTP/2 server finished the handler (it drops its
// response writer state then), as http.Transport does when it closes an
// outbound body asynchronously. It must neither panic nor race.
func TestTransferIdleTimeout_HTTP2BodyUsedAfterHandlerReturned(t *testing.T) {
	bodyCh := make(chan io.ReadCloser, 1)
	returned := make(chan struct{})
	inner := TransferIdleTimeout(testIdle)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyCh <- r.Body
		_, _ = w.Write([]byte("early"))
	}))
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r)
		close(returned)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	pr, pw := io.Pipe()
	defer pw.Close()
	req, err := http.NewRequest(http.MethodPost, srv.URL, pr)
	require.NoError(t, err)
	go func() { _, _ = pw.Write([]byte("partial")) }()
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	body := <-bodyCh
	<-returned
	time.Sleep(100 * time.Millisecond) // let the server loop process handlerDone

	_, _ = body.Read(make([]byte, 8))
	require.NoError(t, body.Close())
	require.NoError(t, body.Close())
}

// trackedPipeServer serves net.Pipe connections and reports when the server
// side closes one, so tests can assert a stalled connection is released.
func trackedPipeServer(t *testing.T, handler http.Handler) (*pipeListener, <-chan struct{}) {
	t.Helper()
	closed := make(chan struct{})
	var once sync.Once
	ln := newPipeListener()
	srv := &http.Server{
		Handler:           TransferIdleTimeout(testIdle)(handler),
		ReadHeaderTimeout: 5 * time.Second,
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				once.Do(func() { close(closed) })
			}
		},
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln, closed
}

func requireClosed(t *testing.T, closed <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(20 * testIdle):
		t.Fatalf("server connection still held: %s", what)
	}
}

// The handler returns with a small unflushed response; net/http's final flush
// then blocks on a client that never reads (net.Pipe has no buffering).
func TestTransferIdleTimeout_FinalFlushToNonReadingClientIsBounded(t *testing.T) {
	ln, closed := trackedPipeServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("small"))
	}))
	conn := ln.dial()
	defer conn.Close()
	_, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	require.NoError(t, err)

	requireClosed(t, closed, "final response flush to a non-reading client")
}

// The client declares a body it never sends and the handler answers without
// reading it; net/http then tries to drain the unread body.
func TestTransferIdleTimeout_UnreadDeclaredBodyDrainIsBounded(t *testing.T) {
	ln, closed := trackedPipeServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	conn := ln.dial()
	defer conn.Close()
	go func() { _, _ = io.Copy(io.Discard, conn) }() // read the response, then stay silent
	_, err := conn.Write([]byte("POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n"))
	require.NoError(t, err)

	requireClosed(t, closed, "drain of an unread declared request body")
}

func TestTransferIdleTimeout_UnreadDeclaredBodyDrainIsBoundedOverTCP(t *testing.T) {
	closed := make(chan struct{})
	var once sync.Once
	srv := httptest.NewUnstartedServer(TransferIdleTimeout(testIdle)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			once.Do(func() { close(closed) })
		}
	}
	srv.Start()
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Write([]byte("POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n"))
	require.NoError(t, err)

	requireClosed(t, closed, "drain of an unread declared request body over TCP")
}

// Leftover deadlines armed at handler return must not leak into the next
// keep-alive request on the same connection.
func TestTransferIdleTimeout_KeepAliveNextRequestNotAffectedByLeftoverDeadlines(t *testing.T) {
	srv := httptest.NewUnstartedServer(TransferIdleTimeout(testIdle)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte("ok"))
	})))
	srv.Config.IdleTimeout = 10 * time.Second
	srv.Start()
	defer srv.Close()

	client := &http.Client{Transport: &http.Transport{MaxConnsPerHost: 1}}
	for i := range 3 {
		resp, err := client.Post(srv.URL, "text/plain", strings.NewReader("body"))
		require.NoError(t, err, "request %d", i)
		_, err = io.ReadAll(resp.Body)
		require.NoError(t, err)
		_ = resp.Body.Close()
		time.Sleep(2 * testIdle) // outlive the leftover deadline while the connection idles
	}
}

// Hijack flushes pending headers; it must not hold the timer's mutex while it
// blocks on a client that does not read.
func TestTransferIdleTimeout_HijackBlockedOnNonReadingClientIsBounded(t *testing.T) {
	hijackErr := make(chan error, 1)
	ln := servePipe(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		conn, _, err := w.(http.Hijacker).Hijack()
		if conn != nil {
			_ = conn.Close()
		}
		hijackErr <- err
	}))
	conn := ln.dial()
	defer conn.Close()
	_, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	require.NoError(t, err)

	select {
	case <-hijackErr:
	case <-time.After(20 * testIdle):
		t.Fatal("Hijack blocked on a non-reading client was not bounded")
	}
}
