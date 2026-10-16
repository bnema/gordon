package app

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/traffic"
)

func writeTimeoutConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestInitConfigHTTPTimeoutDefaults(t *testing.T) {
	_, cfg, err := initConfig(writeTimeoutConfig(t, ""))
	require.NoError(t, err)

	registry, err := buildRegistryHTTPTimeouts(cfg)
	require.NoError(t, err)
	assert.Equal(t, domain.RegistryHTTPTimeouts{
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}, registry)

	entry, err := traffic.ParseHTTPOptions(cfg.Traffic.HTTP)
	require.NoError(t, err)
	assert.Equal(t, domain.HTTPOptions{
		ReadHeaderTimeout:   5 * time.Second,
		IdleTimeout:         30 * time.Second,
		TransferIdleTimeout: 2 * time.Minute,
	}, entry)
}

func TestInitConfigHTTPTimeoutFileOverride(t *testing.T) {
	path := writeTimeoutConfig(t, `
[server.http]
read_header_timeout = "3s"
read_timeout = "1m"
write_timeout = "2m"
idle_timeout = "45s"

[traffic.http]
read_header_timeout = "4s"
idle_timeout = "15s"
transfer_idle_timeout = "10m"
`)
	_, cfg, err := initConfig(path)
	require.NoError(t, err)

	registry, err := buildRegistryHTTPTimeouts(cfg)
	require.NoError(t, err)
	assert.Equal(t, 3*time.Second, registry.ReadHeaderTimeout)
	assert.Equal(t, time.Minute, registry.ReadTimeout)
	assert.Equal(t, 2*time.Minute, registry.WriteTimeout)
	assert.Equal(t, 45*time.Second, registry.IdleTimeout)

	entry, err := traffic.ParseHTTPOptions(cfg.Traffic.HTTP)
	require.NoError(t, err)
	assert.Equal(t, 4*time.Second, entry.ReadHeaderTimeout)
	assert.Equal(t, 15*time.Second, entry.IdleTimeout)
	assert.Equal(t, 10*time.Minute, entry.TransferIdleTimeout)
}

func TestInitConfigHTTPTimeoutEnvOverride(t *testing.T) {
	t.Setenv("GORDON_TRAFFIC_HTTP_TRANSFER_IDLE_TIMEOUT", "7m")
	t.Setenv("GORDON_TRAFFIC_HTTP_READ_HEADER_TIMEOUT", "6s")
	t.Setenv("GORDON_TRAFFIC_HTTP_IDLE_TIMEOUT", "11s")
	t.Setenv("GORDON_SERVER_HTTP_WRITE_TIMEOUT", "9m")
	t.Setenv("GORDON_SERVER_HTTP_READ_TIMEOUT", "8m")
	t.Setenv("GORDON_SERVER_HTTP_READ_HEADER_TIMEOUT", "12s")
	t.Setenv("GORDON_SERVER_HTTP_IDLE_TIMEOUT", "50s")
	t.Setenv("GORDON_TRAFFIC_TCP_DIAL_TIMEOUT", "3s")

	path := writeTimeoutConfig(t, "[traffic.http]\ntransfer_idle_timeout = \"1m\"\n")
	_, cfg, err := initConfig(path)
	require.NoError(t, err)

	entry, err := traffic.ParseHTTPOptions(cfg.Traffic.HTTP)
	require.NoError(t, err)
	assert.Equal(t, 7*time.Minute, entry.TransferIdleTimeout, "env wins over file")
	assert.Equal(t, 6*time.Second, entry.ReadHeaderTimeout)
	assert.Equal(t, 11*time.Second, entry.IdleTimeout)

	registry, err := buildRegistryHTTPTimeouts(cfg)
	require.NoError(t, err)
	assert.Equal(t, 9*time.Minute, registry.WriteTimeout)
	assert.Equal(t, 8*time.Minute, registry.ReadTimeout)
	assert.Equal(t, 12*time.Second, registry.ReadHeaderTimeout)
	assert.Equal(t, 50*time.Second, registry.IdleTimeout)

	// Existing traffic.tcp keys are overridable too (they need a default).
	assert.Equal(t, "3s", cfg.Traffic.TCP.DialTimeout)
}

func TestInitConfigRejectsInvalidHTTPTimeouts(t *testing.T) {
	tests := []struct {
		name string
		body string
		key  string
	}{
		{"malformed registry", "[server.http]\nread_timeout = \"soon\"\n", "server.http.read_timeout"},
		{"negative registry", "[server.http]\nwrite_timeout = \"-5s\"\n", "server.http.write_timeout"},
		{"zero registry", "[server.http]\nidle_timeout = \"0s\"\n", "server.http.idle_timeout"},
		{"malformed traffic", "[traffic.http]\nread_header_timeout = \"x\"\n", "traffic.http.read_header_timeout"},
		{"negative traffic", "[traffic.http]\ntransfer_idle_timeout = \"-1m\"\n", "traffic.http.transfer_idle_timeout"},
		{"zero traffic", "[traffic.http]\nidle_timeout = \"0\"\n", "traffic.http.idle_timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := initConfig(writeTimeoutConfig(t, tt.body))
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.key)
		})
	}
}

func TestInitConfigRejectsInvalidHTTPTimeoutFromEnv(t *testing.T) {
	t.Setenv("GORDON_TRAFFIC_HTTP_TRANSFER_IDLE_TIMEOUT", "-3s")
	_, _, err := initConfig(writeTimeoutConfig(t, ""))
	require.ErrorContains(t, err, "traffic.http.transfer_idle_timeout")
}

func TestStartServerAppliesConfiguredTimeouts(t *testing.T) {
	timeouts := domain.RegistryHTTPTimeouts{
		ReadHeaderTimeout: 1 * time.Second,
		ReadTimeout:       2 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       4 * time.Second,
	}
	errChan := make(chan error, 1)
	srv, ready := startServer("127.0.0.1:0", http.NotFoundHandler(), "test", nil, timeouts, errChan, zerowrap.Default())
	<-ready
	defer func() { _ = srv.Close() }()

	assert.Equal(t, timeouts.ReadHeaderTimeout, srv.ReadHeaderTimeout)
	assert.Equal(t, timeouts.ReadTimeout, srv.ReadTimeout)
	assert.Equal(t, timeouts.WriteTimeout, srv.WriteTimeout)
	assert.Equal(t, timeouts.IdleTimeout, srv.IdleTimeout)
}

func TestStartServerFallsBackToDefaultTimeouts(t *testing.T) {
	errChan := make(chan error, 1)
	srv, ready := startServer("127.0.0.1:0", http.NotFoundHandler(), "test", nil, domain.RegistryHTTPTimeouts{}, errChan, zerowrap.Default())
	<-ready
	defer func() { _ = srv.Close() }()

	assert.Equal(t, domain.DefaultRegistryHTTPTimeouts(), domain.RegistryHTTPTimeouts{
		ReadHeaderTimeout: srv.ReadHeaderTimeout,
		ReadTimeout:       srv.ReadTimeout,
		WriteTimeout:      srv.WriteTimeout,
		IdleTimeout:       srv.IdleTimeout,
	})
}

func TestStartRegistryServersRejectsInvalidTimeouts(t *testing.T) {
	cfg := Config{}
	cfg.Server.HTTP.ReadTimeout = "-1s"
	_, _, _, _, err := startRegistryServers(cfg, http.NotFoundHandler(), make(chan error, 1), zerowrap.Default())
	require.ErrorContains(t, err, "server.http.read_timeout")
}
