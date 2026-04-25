package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/http/registry"
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

type coordinatorRecorder struct {
	mu           sync.Mutex
	triggerCalls int
	applyCalls   int
	err          error
}

func (c *coordinatorRecorder) Trigger(context.Context) error {
	c.mu.Lock()
	c.triggerCalls++
	err := c.err
	c.mu.Unlock()
	return err
}

func (c *coordinatorRecorder) ApplyLoadedConfig(context.Context) error {
	c.mu.Lock()
	c.applyCalls++
	err := c.err
	c.mu.Unlock()
	return err
}

func (c *coordinatorRecorder) TriggerCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.triggerCalls
}

func (c *coordinatorRecorder) ApplyCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.applyCalls
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
	err       error
}

func (e *eventBusRecorder) Publish(eventType domain.EventType, payload any) error {
	e.mu.Lock()
	e.calls++
	e.eventType = eventType
	e.payload = payload
	err := e.err
	e.mu.Unlock()
	return err
}

func (e *eventBusRecorder) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func TestSetupConfigHotReload_WatchCallbackInvokesCoordinator(t *testing.T) {
	ctx := context.Background()
	configSvc := &watchRecorder{}
	coordinator := &coordinatorRecorder{}

	err := setupConfigHotReload(ctx, configSvc, coordinator)
	require.NoError(t, err)
	require.True(t, configSvc.called)
	require.NotNil(t, configSvc.onChange)

	configSvc.onChange()
	require.Equal(t, 1, coordinator.ApplyCalls())
	require.Equal(t, 0, coordinator.TriggerCalls())
}

func TestBuildProxyConfig_ParsesMaxBlobSizeWithoutChangingChunkDefault(t *testing.T) {
	cfg := Config{}
	cfg.Server.MaxBlobSize = "2GB"

	result, err := buildProxyConfig(cfg, zerowrap.Default())

	require.NoError(t, err)
	assert.Equal(t, int64(2<<30), result.maxBlobSize)
	assert.Equal(t, int64(registry.DefaultMaxBlobChunkSize), result.maxBlobChunkSize)
}

func TestBuildProxyConfig_DefaultMaxBlobSize(t *testing.T) {
	result, err := buildProxyConfig(Config{}, zerowrap.Default())

	require.NoError(t, err)
	assert.Equal(t, int64(registry.DefaultMaxBlobSize), result.maxBlobSize)
}

func TestReloadCoordinator_ApplyLoadedConfig_RebuildsProxyConfigAndPublishesEvent(t *testing.T) {
	ctx := context.Background()
	v := viper.New()
	v.Set("server.gordon_domain", "reload.example.com")
	v.Set("server.registry_port", 5000)
	v.Set("server.max_proxy_body_size", "5MB")
	v.Set("server.max_blob_chunk_size", "6MB")
	v.Set("server.max_proxy_response_size", "7MB")
	v.Set("server.max_concurrent_connections", 99)

	reloadSvc := &reloadRecorder{}
	proxySvc := &proxyRecorder{}
	events := &eventBusRecorder{}

	coord := newReloadCoordinator(v, reloadSvc, proxySvc, events, zerowrap.Default())
	require.NoError(t, coord.ApplyLoadedConfig(ctx))

	require.Equal(t, 0, reloadSvc.Calls())
	require.Equal(t, 1, proxySvc.calls)
	require.Equal(t, 1, events.Calls())
	require.Equal(t, domain.EventConfigReload, events.eventType)
	require.Equal(t, proxy.Config{
		RegistryDomain:     "reload.example.com",
		RegistryPort:       5000,
		MaxBodySize:        5 << 20,
		MaxResponseSize:    7 << 20,
		MaxConcurrentConns: 99,
	}, proxySvc.config)
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

func TestReloadCoordinator_RetriesImmediatelyAfterFailedReload(t *testing.T) {
	ctx := context.Background()
	v := viper.New()
	v.Set("server.gordon_domain", "retry.example.com")
	v.Set("server.registry_port", 5000)
	v.Set("server.max_proxy_body_size", "2MB")
	v.Set("server.max_blob_chunk_size", "3MB")
	v.Set("server.max_proxy_response_size", "4MB")
	v.Set("server.max_concurrent_connections", 11)

	reloadErr := errors.New("reload failed")
	reloadSvc := &reloadRecorder{err: reloadErr}
	proxySvc := &proxyRecorder{}
	coord := newReloadCoordinator(v, reloadSvc, proxySvc, nil, zerowrap.Default())

	err := coord.Trigger(ctx)
	require.ErrorIs(t, err, reloadErr)

	reloadSvc.mu.Lock()
	reloadSvc.err = nil
	reloadSvc.mu.Unlock()

	require.NoError(t, coord.Trigger(ctx))
	require.Equal(t, 2, reloadSvc.Calls())
	assert.Equal(t, 1, proxySvc.calls)
}

func TestReloadCoordinator_PublishErrorDoesNotAdvanceDebounceState(t *testing.T) {
	ctx := context.Background()
	v := viper.New()
	v.Set("server.gordon_domain", "publish.example.com")
	v.Set("server.registry_port", 5000)
	v.Set("server.max_proxy_body_size", "5MB")
	v.Set("server.max_blob_chunk_size", "6MB")
	v.Set("server.max_proxy_response_size", "7MB")
	v.Set("server.max_concurrent_connections", 99)

	publishErr := errors.New("publish failed")
	reloadSvc := &reloadRecorder{}
	proxySvc := &proxyRecorder{}
	events := &eventBusRecorder{err: publishErr}

	coord := newReloadCoordinator(v, reloadSvc, proxySvc, events, zerowrap.Default())

	err := coord.Trigger(ctx)
	require.ErrorIs(t, err, publishErr)

	err = coord.Trigger(ctx)
	require.ErrorIs(t, err, publishErr)

	require.Equal(t, 2, reloadSvc.Calls())
	require.Equal(t, 2, proxySvc.calls)
	require.Equal(t, 2, events.Calls())
	require.Equal(t, domain.EventConfigReload, events.eventType)
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
