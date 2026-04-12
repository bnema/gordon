package app

import (
	"context"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

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

type proxyRecorder struct {
	calls  int
	config proxy.Config
}

func (p *proxyRecorder) UpdateConfig(config proxy.Config) {
	p.calls++
	p.config = config
}

func TestSetupConfigHotReloadUsesConfigWatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	v := viper.New()
	configSvc := &watchRecorder{}
	proxySvc := &proxyRecorder{}

	err := setupConfigHotReload(ctx, v, configSvc, proxySvc, zerowrap.Default())
	require.NoError(t, err)
	require.True(t, configSvc.called)
}

func TestSetupConfigHotReloadRebuildsProxyConfigFromUpdatedViperState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	v := viper.New()
	v.Set("server.gordon_domain", "old.example.com")
	v.Set("server.registry_port", 5000)
	v.Set("server.max_proxy_body_size", "2MB")
	v.Set("server.max_blob_chunk_size", "3MB")
	v.Set("server.max_proxy_response_size", "4MB")
	v.Set("server.max_concurrent_connections", 11)

	configSvc := &watchRecorder{}
	proxySvc := &proxyRecorder{}

	err := setupConfigHotReload(ctx, v, configSvc, proxySvc, zerowrap.Default())
	require.NoError(t, err)
	require.NotNil(t, configSvc.onChange)

	v.Set("server.gordon_domain", "new.example.com")
	v.Set("server.registry_port", 5050)
	v.Set("server.max_proxy_body_size", "5MB")
	v.Set("server.max_blob_chunk_size", "6MB")
	v.Set("server.max_proxy_response_size", "7MB")
	v.Set("server.max_concurrent_connections", 99)

	configSvc.onChange()

	require.Equal(t, 1, proxySvc.calls)
	require.Equal(t, proxy.Config{
		RegistryDomain:     "new.example.com",
		RegistryPort:       5050,
		MaxBodySize:        5 << 20,
		MaxResponseSize:    7 << 20,
		MaxConcurrentConns: 99,
	}, proxySvc.config)
}
