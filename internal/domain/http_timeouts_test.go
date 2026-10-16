package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultHTTPOptions(t *testing.T) {
	assert.Equal(t, HTTPOptions{
		ReadHeaderTimeout:   5 * time.Second,
		IdleTimeout:         30 * time.Second,
		TransferIdleTimeout: 2 * time.Minute,
	}, DefaultHTTPOptions())
	assert.NoError(t, DefaultHTTPOptions().Validate())
}

func TestHTTPOptionsWithDefaults(t *testing.T) {
	got := HTTPOptions{TransferIdleTimeout: time.Hour}.WithDefaults()
	assert.Equal(t, 5*time.Second, got.ReadHeaderTimeout)
	assert.Equal(t, 30*time.Second, got.IdleTimeout)
	assert.Equal(t, time.Hour, got.TransferIdleTimeout)
}

func TestHTTPOptionsValidateRejectsNegative(t *testing.T) {
	require.ErrorContains(t, HTTPOptions{ReadHeaderTimeout: -1}.Validate(), "traffic.http.read_header_timeout")
	require.ErrorContains(t, HTTPOptions{IdleTimeout: -1}.Validate(), "traffic.http.idle_timeout")
	require.ErrorContains(t, HTTPOptions{TransferIdleTimeout: -1}.Validate(), "traffic.http.transfer_idle_timeout")
	assert.NoError(t, HTTPOptions{}.Validate(), "zero selects the default")
}

func TestTrafficGraphRejectsNegativeHTTPOptions(t *testing.T) {
	graph := TrafficGraph{Options: TrafficOptions{HTTP: HTTPOptions{TransferIdleTimeout: -time.Second}}}
	require.ErrorContains(t, graph.Validate(), "traffic.http.transfer_idle_timeout")
}

func TestDefaultRegistryHTTPTimeouts(t *testing.T) {
	assert.Equal(t, RegistryHTTPTimeouts{
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}, DefaultRegistryHTTPTimeouts())
	assert.Equal(t, DefaultRegistryHTTPTimeouts(), RegistryHTTPTimeouts{}.WithDefaults())
}

func TestRegistryHTTPTimeoutsValidate(t *testing.T) {
	assert.NoError(t, DefaultRegistryHTTPTimeouts().Validate())
	require.ErrorContains(t, RegistryHTTPTimeouts{ReadHeaderTimeout: -1}.Validate(), "server.http.read_header_timeout")
	require.ErrorContains(t, RegistryHTTPTimeouts{ReadTimeout: -1}.Validate(), "server.http.read_timeout")
	require.ErrorContains(t, RegistryHTTPTimeouts{WriteTimeout: -1}.Validate(), "server.http.write_timeout")
	require.ErrorContains(t, RegistryHTTPTimeouts{IdleTimeout: -1}.Validate(), "server.http.idle_timeout")
}
