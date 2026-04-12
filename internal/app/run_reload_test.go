package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/proxy"
)

type watchRecorder struct {
	called   bool
	onChange func()
}

func (w *watchRecorder) Watch(ctx context.Context, onChange func()) error {
	w.called = true
	w.onChange = onChange
	return nil
}

type triggerRecorder struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (t *triggerRecorder) Trigger(context.Context) error {
	t.mu.Lock()
	t.calls++
	err := t.err
	t.mu.Unlock()
	return err
}

func (t *triggerRecorder) Calls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

type reloadRecorder struct {
	mu       sync.Mutex
	calls    int
	err      error
	onReload func()
}

func (r *reloadRecorder) Reload(context.Context) error {
	r.mu.Lock()
	r.calls++
	err := r.err
	onReload := r.onReload
	r.mu.Unlock()

	if onReload != nil {
		onReload()
	}

	return err
}

func (r *reloadRecorder) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type proxyRecorder struct {
	calls  int
	config proxy.Config
}

func (p *proxyRecorder) UpdateConfig(config proxy.Config) {
	p.calls++
	p.config = config
}

type eventBusRecorder struct {
	mu        sync.Mutex
	calls     int
	eventType domain.EventType
	payload   any
}

func (e *eventBusRecorder) Publish(eventType domain.EventType, payload any) error {
	e.mu.Lock()
	e.calls++
	e.eventType = eventType
	e.payload = payload
	e.mu.Unlock()
	return nil
}

func (e *eventBusRecorder) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func TestSetupConfigHotReload_UsesSharedCoordinator(t *testing.T) {
	ctx := context.Background()
	configSvc := &watchRecorder{}
	coordinator := &triggerRecorder{}

	err := setupConfigHotReload(ctx, configSvc, coordinator)
	require.NoError(t, err)
	require.True(t, configSvc.called)
	require.NotNil(t, configSvc.onChange)

	configSvc.onChange()
	require.Equal(t, 1, coordinator.Calls())
}

func TestReloadCoordinator_DebouncesRepeatedWatchCallbacks(t *testing.T) {
	ctx := context.Background()
	v := viper.New()
	v.Set("server.gordon_domain", "new.example.com")
	v.Set("server.registry_port", 5000)
	v.Set("server.max_proxy_body_size", "5MB")
	v.Set("server.max_blob_chunk_size", "6MB")
	v.Set("server.max_proxy_response_size", "7MB")
	v.Set("server.max_concurrent_connections", 99)

	reloadSvc := &reloadRecorder{}
	proxySvc := &proxyRecorder{}
	events := &eventBusRecorder{}

	coord := newReloadCoordinator(v, reloadSvc, proxySvc, events, zerowrap.Default())
	require.NoError(t, coord.Trigger(ctx))
	require.NoError(t, coord.Trigger(ctx))

	require.Equal(t, 1, reloadSvc.Calls())
	require.Equal(t, 1, proxySvc.calls)
	require.Equal(t, 1, events.Calls())
	require.Equal(t, domain.EventConfigReload, events.eventType)
	require.Equal(t, proxy.Config{
		RegistryDomain:     "new.example.com",
		RegistryPort:       5000,
		MaxBodySize:        5 << 20,
		MaxResponseSize:    7 << 20,
		MaxConcurrentConns: 99,
	}, proxySvc.config)
}

func TestReloadCoordinator_SerializesOverlappingReloadRequests(t *testing.T) {
	ctx := context.Background()
	v := viper.New()
	v.Set("server.gordon_domain", "app.example.com")
	v.Set("server.registry_port", 5000)
	v.Set("server.max_proxy_body_size", "2MB")
	v.Set("server.max_blob_chunk_size", "3MB")
	v.Set("server.max_proxy_response_size", "4MB")
	v.Set("server.max_concurrent_connections", 11)

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	reloadSvc := &reloadRecorder{onReload: func() {
		once.Do(func() { close(started) })
		<-release
	}}
	proxySvc := &proxyRecorder{}
	coord := newReloadCoordinator(v, reloadSvc, proxySvc, nil, zerowrap.Default())

	firstDone := make(chan struct{})
	go func() {
		_ = coord.Trigger(ctx)
		close(firstDone)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first reload did not start")
	}

	secondDone := make(chan struct{})
	go func() {
		_ = coord.Trigger(ctx)
		close(secondDone)
	}()

	select {
	case <-secondDone:
		t.Fatal("second reload finished before the first was released")
	case <-time.After(75 * time.Millisecond):
	}

	require.Equal(t, 1, reloadSvc.Calls())
	close(release)

	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first reload did not finish")
	}

	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("second reload did not finish")
	}

	require.Equal(t, 1, reloadSvc.Calls())
	assert.Equal(t, 1, proxySvc.calls)
}
