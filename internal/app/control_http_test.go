package app

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlHTTPListenerRequiresDistinctExplicitPlaintextConfiguration(t *testing.T) {
	cfg := Config{Control: ControlConfig{ListenAddress: "127.0.0.1:9090"}}
	cfg.Control.HTTP.ListenAddress = "127.0.0.1:9090"
	cfg.Control.HTTP.InsecureTLS = true
	listener, err := controlHTTPListener(cfg, net.Listen)
	require.Error(t, err)
	assert.Nil(t, listener)
	assert.Contains(t, err.Error(), "must not share")

	cfg.Control.HTTP.ListenAddress = "127.0.0.1:0"
	listener, err = controlHTTPListener(cfg, net.Listen)
	require.NoError(t, err)
	require.NotNil(t, listener)
	assert.NoError(t, listener.Close())
}

func TestControlHTTPListenerRejectsImplicitPlaintext(t *testing.T) {
	cfg := Config{Control: ControlConfig{ListenAddress: "127.0.0.1:9090"}}
	cfg.Control.HTTP.ListenAddress = "127.0.0.1:0"
	listener, err := controlHTTPListener(cfg, net.Listen)
	require.Error(t, err)
	assert.Nil(t, listener)
	assert.Contains(t, err.Error(), "control.http.insecure_tls=true")
}
