package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildDNSConfigFromConfig(t *testing.T) {
	cfg := Config{}
	cfg.DNS.Resolvers = []string{"9.9.9.9:53", "1.1.1.1:53"}
	cfg.DNS.PropagationTimeout = "10m"
	cfg.DNS.PollingInterval = "10s"

	dnsCfg, err := buildDNSConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, []string{"9.9.9.9:53", "1.1.1.1:53"}, dnsCfg.Resolvers)
	assert.Equal(t, 10*time.Minute, dnsCfg.PropagationTimeout)
	assert.Equal(t, 10*time.Second, dnsCfg.PollingInterval)
}

func TestBuildDNSConfigRejectsInvalidDuration(t *testing.T) {
	cfg := Config{}
	cfg.DNS.PropagationTimeout = "not-a-duration"

	_, err := buildDNSConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dns.propagation_timeout")
}
