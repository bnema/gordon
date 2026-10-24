package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitConfigHandoverDefaults(t *testing.T) {
	_, cfg, err := initConfig(writeTimeoutConfig(t, ""))
	require.NoError(t, err)
	assert.Equal(t, 90*time.Second, cfg.Handover.MaxHold)
	assert.Equal(t, 1024, cfg.Handover.MaxHeldRequests)
}

func TestInitConfigHandoverEnv(t *testing.T) {
	t.Setenv("GORDON_TRAFFIC_HANDOVER_MAX_HOLD", "12s")
	t.Setenv("GORDON_TRAFFIC_HANDOVER_MAX_HELD_REQUESTS", "24")
	_, cfg, err := initConfig(writeTimeoutConfig(t, ""))
	require.NoError(t, err)
	assert.Equal(t, 12*time.Second, cfg.Handover.MaxHold)
	assert.Equal(t, 24, cfg.Handover.MaxHeldRequests)
}

func TestInitConfigHandoverInvalid(t *testing.T) {
	for _, body := range []string{
		"max_hold = \"bad\"", "max_hold = \"-1s\"", "max_held_requests = -1",
	} {
		t.Run(body, func(t *testing.T) {
			_, _, err := initConfig(writeTimeoutConfig(t, "[traffic.handover]\n"+body))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "traffic.handover.")
		})
	}
}

func TestInitConfigHandoverDisabled(t *testing.T) {
	_, cfg, err := initConfig(writeTimeoutConfig(t, "[traffic.handover]\nmax_hold = \"0s\""))
	require.NoError(t, err)
	assert.Zero(t, cfg.Handover.MaxHold)
}
