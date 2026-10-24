package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/http/httphelper"
	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/proxy"
)

type blockingReloader struct {
	entered chan context.Context
	release chan struct{}
}

func (r *blockingReloader) Reload(ctx context.Context) error {
	r.entered <- ctx
	<-r.release // Model a reload dependency that ignores cancellation until released.
	return nil
}

func TestReloadStopDoesNotWaitForRunningReload(t *testing.T) {
	r := &blockingReloader{make(chan context.Context, 1), make(chan struct{})}
	c := newReloadCoordinator(nil, r, nil, nil, nil, nil, nil, zerowrap.Default())
	done := make(chan error, 1)
	go func() { done <- c.Trigger(context.Background()) }()
	var reloadCtx context.Context
	select {
	case reloadCtx = <-r.entered:
	case <-time.After(time.Second):
		t.Fatal("reload did not start")
	}
	stopped := make(chan struct{})
	go func() { c.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked behind reload")
	}
	select {
	case <-reloadCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("reload context not cancelled")
	}
	close(r.release)
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("reload did not finish")
	}
	require.ErrorIs(t, c.Trigger(context.Background()), context.Canceled)
}

func TestWatcherCallbackSuppressedAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &watchRecorder{}
	c := &coordinatorRecorder{}
	require.NoError(t, setupConfigHotReload(ctx, w, c))
	cancel()
	w.onChange()
	assert.Zero(t, c.ApplyCalls())
}

func TestDrainIngressForceClosesLongRequest(t *testing.T) {
	entered := make(chan struct{})
	exited := make(chan struct{})
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(exited)
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(listener) }()
	defer srv.Close()
	conn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: example.test\r\n\r\n"))
	require.NoError(t, err)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	cfg := Config{}
	cfg.Server.ShutdownDrainTimeout = "30ms"
	timeout, err := buildShutdownDrainTimeout(cfg)
	require.NoError(t, err)
	started := time.Now()
	drainIngress([]*http.Server{srv}, nil, nil, nil, nil, timeout, zerowrap.Default())
	assert.GreaterOrEqual(t, time.Since(started), timeout)
	assert.Less(t, time.Since(started), time.Second)
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("request was not force-closed")
	}
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, err = conn.Read(make([]byte, 1))
	require.Error(t, err)
}

func TestShutdownDrainConfig(t *testing.T) {
	_, cfg, err := initConfig(writeTimeoutConfig(t, ""))
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, drainTimeout(cfg))
	for _, value := range []string{"0s", "-1s", "soon", "6m"} {
		t.Run(value, func(t *testing.T) {
			_, _, err := initConfig(writeTimeoutConfig(t, "[server]\nshutdown_drain_timeout = \""+value+"\"\n"))
			require.ErrorContains(t, err, "server.shutdown_drain_timeout")
		})
	}
	t.Setenv("GORDON_SERVER_SHUTDOWN_DRAIN_TIMEOUT", "2s")
	_, cfg, err = initConfig(writeTimeoutConfig(t, "[server]\nshutdown_drain_timeout = \"3s\"\n"))
	require.NoError(t, err)
	assert.Equal(t, 2*time.Second, drainTimeout(cfg))
}

func TestDrainIngressIdleReturnsWithoutWaitingForDeadline(t *testing.T) {
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.NotFoundHandler()}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(listener) }()
	defer srv.Close()
	// Confirm Serve registered the listener before asking it to shut down.
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + listener.Addr().String())
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	client.CloseIdleConnections()
	started := time.Now()
	drainIngress([]*http.Server{srv}, nil, nil, nil, nil, 5*time.Second, zerowrap.Default())
	assert.Less(t, time.Since(started), time.Second)
	_, err = net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	require.Error(t, err)
}

type signalBlockingTrigger struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (r *signalBlockingTrigger) Trigger(ctx context.Context) error {
	if r.calls.Add(1) == 1 {
		close(r.entered)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.release:
		return nil
	}
}

func TestSignalReloadWorkerCoalescesAndStops(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "one pending", true: "cancel blocked"}[stop], func(t *testing.T) {
			r := &signalBlockingTrigger{entered: make(chan struct{}), release: make(chan struct{}, 2)}
			w := newSignalReloadWorker(context.Background(), r)
			defer w.Stop()
			w.Request()
			select {
			case <-r.entered:
			case <-time.After(time.Second):
				t.Fatal("worker not started")
			}
			for range 1000 {
				w.Request()
			}
			require.Len(t, w.pending, 1)
			if !stop {
				r.release <- struct{}{}
				require.Eventually(t, func() bool { return r.calls.Load() == 2 }, time.Second, time.Millisecond)
			}
			w.Stop()
			select {
			case <-w.done:
			case <-time.After(time.Second):
				t.Fatal("reload worker leaked")
			}
			if stop {
				assert.EqualValues(t, 1, r.calls.Load())
			} else {
				assert.EqualValues(t, 2, r.calls.Load())
			}
		})
	}
}

func TestDrainIngressKeepsRegistryAvailableForAdmittedFrontend(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("registry ok")) }))
	defer backend.Close()
	admitted, proceed := make(chan struct{}), make(chan struct{})
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(admitted)
		<-proceed
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, backend.URL, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, "Registry Unavailable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(w, resp.Body)
	}))
	defer frontend.Close()
	response := make(chan string, 1)
	go func() {
		resp, err := http.Get(frontend.URL)
		if err != nil {
			response <- err.Error()
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		response <- string(body)
	}()
	select {
	case <-admitted:
	case <-time.After(time.Second):
		t.Fatal("request not admitted")
	}
	closing := make(chan struct{})
	frontend.Config.RegisterOnShutdown(func() { close(closing) })
	done := make(chan struct{})
	go func() {
		drainIngress([]*http.Server{backend.Config}, frontend.Config, nil, nil, nil, time.Second, zerowrap.Default())
		close(done)
	}()
	select {
	case <-closing:
	case <-time.After(time.Second):
		t.Fatal("drain not started")
	}
	close(proceed)
	select {
	case body := <-response:
		assert.Equal(t, "registry ok", body)
	case <-time.After(2 * time.Second):
		t.Fatal("request did not finish")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not finish")
	}
}

func TestSignalReloadWorkerStopsWhileCoordinatorBusy(t *testing.T) {
	r := &blockingReloader{make(chan context.Context, 1), make(chan struct{})}
	c := newReloadCoordinator(nil, r, nil, nil, nil, nil, nil, zerowrap.Default())
	running := make(chan error, 1)
	go func() { running <- c.Trigger(context.Background()) }()
	select {
	case <-r.entered:
	case <-time.After(time.Second):
		t.Fatal("reload not started")
	}
	w := newSignalReloadWorker(context.Background(), c)
	defer w.Stop()
	for range 1000 {
		w.Request()
	}
	w.Stop()
	c.Stop()
	select {
	case <-w.done:
	case <-time.After(time.Second):
		t.Fatal("worker stuck waiting for reload serialization")
	}
	close(r.release)
	select {
	case err := <-running:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("reload did not unwind")
	}
}

func TestTrailingReloadWaitingBehindBlockedReloadExitsOnStop(t *testing.T) {
	r := &blockingReloader{make(chan context.Context, 1), make(chan struct{})}
	c := newReloadCoordinator(nil, r, nil, nil, nil, nil, nil, zerowrap.Default())
	running := make(chan error, 1)
	go func() { running <- c.Trigger(context.Background()) }()
	select {
	case <-r.entered:
	case <-time.After(time.Second):
		t.Fatal("reload did not start")
	}
	trailingCtx := c.reloadContext(context.Background())
	entered, done := make(chan struct{}), make(chan struct{})
	go func() {
		close(entered)
		c.runTrailingReload(trailingCtx, 0, true)
		close(done)
	}()
	<-entered
	c.Stop()
	// The dependency is still blocked: the trailing callback must not need its
	// serialization lock to observe lifecycle cancellation and exit.
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("trailing callback stuck behind reload after Stop")
	}
	close(r.release)
	select {
	case err := <-running:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("reload did not unwind")
	}
}

func TestSmartTCPOrderedDrainIdle(t *testing.T) {
	for _, keepAlive := range []bool{false, true} {
		t.Run(fmt.Sprint(keepAlive), func(t *testing.T) {
			manager := trafficadapter.NewManager()
			manager.SetSmartTCPHTTPServer("edge", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }), nil)
			graph := domain.TrafficGraph{EntryPoints: []domain.EntryPoint{{Name: "edge", Address: "127.0.0.1:0", Protocol: domain.EntryPointProtocolSmartTCP}}}
			// Use a concrete address to dial the manager's externally owned socket.
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			graph.EntryPoints[0].Address = ln.Addr().String()
			require.NoError(t, ln.Close())
			require.NoError(t, manager.Apply(context.Background(), &graph))
			if keepAlive {
				conn, err := net.Dial("tcp", graph.EntryPoints[0].Address)
				require.NoError(t, err)
				defer conn.Close()
				_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: example.test\r\n\r\n"))
				require.NoError(t, err)
				resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
				require.NoError(t, err)
				_, err = io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
			}
			started := time.Now()
			manager.StopAccepting()
			drainIngress(nil, nil, nil, proxy.NewService(nil, proxy.Config{}), manager, 5*time.Second, zerowrap.Default())
			assert.Less(t, time.Since(started), 500*time.Millisecond)
		})
	}
}

func TestOrderedDrainUnreadHTTPConnection(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	accepted := make(chan struct{}, 1)
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			accepted <- struct{}{}
		}
	}
	httphelper.TrackHTTPDrain(srv.Config)
	srv.Start()
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	<-accepted
	started := time.Now()
	drainIngress([]*http.Server{srv.Config}, nil, nil, nil, nil, 5*time.Second, zerowrap.Default())
	assert.Less(t, time.Since(started), 500*time.Millisecond)
}
