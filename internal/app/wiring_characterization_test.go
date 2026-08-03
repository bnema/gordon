package app

import (
	"context"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/usecase/traffic"
)

func TestMonolithServiceGraphInventory(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	v := viper.New()
	v.Set("server.data_dir", dataDir)
	v.Set("server.gordon_domain", "gordon.test")
	v.Set("server.registry_port", 5000)
	v.Set("auth.secrets_backend", "unsafe")
	v.Set("logging.file.enabled", false)

	cfg := Config{}
	cfg.Server.DataDir = dataDir
	cfg.Server.GordonDomain = "gordon.test"
	cfg.Server.RegistryPort = 5000
	cfg.Auth.SecretsBackend = "unsafe"
	cfg.Logging.File.Enabled = false
	cfg.EntryPoints = map[string]traffic.EntryPointConfig{
		traffic.DefaultEdgeEntryPointName: {Address: ":0", Protocol: "smart_tcp"},
	}

	svc, err := createServices(ctx, v, cfg, zerowrap.Default())
	if err != nil {
		t.Skipf("container runtime unavailable for monolith service graph inventory: %v", err)
	}
	t.Cleanup(func() {
		if svc.pkiSvc != nil {
			svc.pkiSvc.Stop()
		}
		if svc.trafficManager != nil {
			require.NoError(t, svc.trafficManager.Shutdown(ctx))
		}
	})

	registryHandler, httpProxyHandler, httpsProxyHandler := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	require.NotNil(t, svc.configSvc, "config service")
	require.NotNil(t, svc.eventBus, "event bus")
	require.NotNil(t, svc.registrySvc, "registry service")
	require.NotNil(t, svc.containerSvc, "container service")
	require.NotNil(t, svc.proxySvc, "proxy service")
	require.NotNil(t, svc.healthSvc, "health service")
	require.NotNil(t, svc.logSvc, "log service")
	require.NotNil(t, svc.imageSvc, "image service")
	require.NotNil(t, svc.volumeSvc, "volume service")
	require.NotNil(t, svc.adminHandler, "admin handler")
	require.NotNil(t, registryHandler, "registry handler graph")
	require.NotNil(t, httpProxyHandler, "http proxy handler graph")
	require.NotNil(t, httpsProxyHandler, "https proxy handler graph")
	require.NotNil(t, svc.trafficManager, "traffic manager for TLS-capable entrypoint")
}
