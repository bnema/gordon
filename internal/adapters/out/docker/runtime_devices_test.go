package docker

import (
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
}
