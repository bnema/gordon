package docker

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func podmanVersion(version string) client.ServerVersionResult {
	return client.ServerVersionResult{
		Version:    version,
		Components: []system.ComponentVersion{{Name: "Podman Engine", Version: version}},
	}
}

func dockerVersion(version string) client.ServerVersionResult {
	return client.ServerVersionResult{
		Version:    version,
		Components: []system.ComponentVersion{{Name: "Engine", Version: version}},
	}
}

func TestBuildDeviceRequests_EmptyIsNil(t *testing.T) {
	assert.Nil(t, buildDeviceRequests(&domain.ContainerConfig{}))
	assert.Nil(t, buildDeviceRequests(&domain.ContainerConfig{CDIDevices: nil}))
}

func TestBuildDeviceRequests_OneNativeCDIRequest(t *testing.T) {
	requests := buildDeviceRequests(&domain.ContainerConfig{
		CDIDevices: []string{"example.com/gpu=GPU-b", "example.com/gpu=GPU-a"},
	})
	require.Len(t, requests, 1, "all CDI IDs travel in one native DeviceRequest")
	request := requests[0]
	assert.Equal(t, container.DeviceRequest{
		Driver:    "cdi",
		DeviceIDs: []string{"example.com/gpu=GPU-a", "example.com/gpu=GPU-b"},
	}, request, "driver is cdi, capabilities/options stay empty, count stays 0, IDs sorted")
}

func TestBuildDeviceRequests_DoesNotMutateConfig(t *testing.T) {
	config := &domain.ContainerConfig{CDIDevices: []string{"example.com/gpu=GPU-b", "example.com/gpu=GPU-a"}}
	_ = buildDeviceRequests(config)
	assert.Equal(t, []string{"example.com/gpu=GPU-b", "example.com/gpu=GPU-a"}, config.CDIDevices)
}

func TestCheckCDISupportMatrix(t *testing.T) {
	cases := []struct {
		name      string
		version   client.ServerVersionResult
		hint      string
		wantError bool
	}{
		{"podman 6.1.1", podmanVersion("6.1.1"), "", false},
		{"podman 5.4 exact", podmanVersion("5.4.0"), "", false},
		{"podman 4.9 refused", podmanVersion("4.9.5"), "", true},
		{"docker 28.3", dockerVersion("28.3.2"), "", false},
		{"docker 27 refused", dockerVersion("27.5.1"), "", true},
		{"unknown engine refused", client.ServerVersionResult{Version: "9.9.9"}, "unknown", true},
		{"socket hint podman accepted", client.ServerVersionResult{Version: "6.1.1"}, "podman", false},
		{"unparsable version refused", podmanVersion("not-a-version"), "", true},
		{"prerelease minor refused", podmanVersion("5.4-rc1"), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkCDISupport(tc.version, tc.hint)
			if tc.wantError {
				require.Error(t, err)
				assert.ErrorIs(t, err, domain.ErrRuntimeUnsupported)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCompareEngineVersion(t *testing.T) {
	assert.Greater(t, compareEngineVersion("6.1.1", "5.4"), 0)
	assert.Less(t, compareEngineVersion("4.9.5", "5.4"), 0)
	assert.Equal(t, 0, compareEngineVersion("5.4.0", "5.4"))
	assert.Less(t, compareEngineVersion("not-a-version", "5.4"), 0)
	assert.Equal(t, 0, compareEngineVersion("28.3.2", "28.3"), "patch releases do not affect the gate")

	// A prerelease suffix must never lift an engine above the gate: the
	// minor component has to be numeric on its own.
	assert.Less(t, compareEngineVersion("5.4-rc1", "5.4"), 0)
	assert.Less(t, compareEngineVersion("28.3-rc1", "28.3"), 0)
	assert.Less(t, compareEngineVersion("5.", "5.4"), 0)
}

// TestEngineProbeError proves a failed /version probe keeps cancellation
// recognizable while every other cause collapses to the unsupported
// sentinel without the daemon endpoint in the text.
func TestEngineProbeError(t *testing.T) {
	t.Run("canceled probe keeps the sentinel and drops the endpoint", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := engineProbeError(ctx, errors.New("dial unix /run/podman/podman.sock: i/o timeout"))
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorIs(t, err, domain.ErrRuntimeUnsupported)
		assert.NotContains(t, err.Error(), "podman.sock")
	})

	t.Run("canceled adapter cause is preserved", func(t *testing.T) {
		err := engineProbeError(context.Background(), fmt.Errorf("probe: %w", context.Canceled))
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("other probe failures stay redacted", func(t *testing.T) {
		err := engineProbeError(context.Background(), errors.New("dial unix /run/podman/podman.sock: connection refused"))
		require.ErrorIs(t, err, domain.ErrRuntimeUnsupported)
		assert.False(t, errors.Is(err, context.Canceled))
		assert.NotContains(t, err.Error(), "podman.sock")
	})
}
