package docker

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/moby/moby/api/types/container"

	"github.com/bnema/gordon/internal/domain"
)

// TestBuildNetworkProbeCommand proves the helper argv carries the exact
// target and clamps the timeout to whole seconds within its bound.
func TestBuildNetworkProbeCommand(t *testing.T) {
	t.Run("http carries path, whole-second timeout, and exact endpoint", func(t *testing.T) {
		command := buildNetworkProbeCommand(domain.ContainerNetworkProbeRequest{
			Protocol: domain.ProbeProtocolHTTP,
			Path:     "/healthz",
			Port:     8080,
			Timeout:  1500 * time.Millisecond,
		}, "172.18.0.5")
		assert.Equal(t, httpProbeScript, command.script)
		assert.Equal(t, []string{"/healthz", "1", "172.18.0.5", "8080"}, command.args)
	})

	t.Run("tcp carries no path", func(t *testing.T) {
		command := buildNetworkProbeCommand(domain.ContainerNetworkProbeRequest{
			Protocol: domain.ProbeProtocolTCP,
			Port:     5432,
			Timeout:  time.Second,
		}, "172.18.0.6")
		assert.Equal(t, tcpProbeScript, command.script)
		assert.Equal(t, []string{"1", "172.18.0.6", "5432"}, command.args)
	})

	t.Run("nc bound stays strictly under the attempt bound", func(t *testing.T) {
		for _, timeout := range []time.Duration{1500 * time.Millisecond, 5 * time.Second, 30 * time.Second} {
			command := buildNetworkProbeCommand(domain.ContainerNetworkProbeRequest{
				Protocol: domain.ProbeProtocolTCP, Port: 1, Timeout: timeout,
			}, "10.0.0.1")
			seconds, err := strconv.Atoi(command.args[0])
			require.NoError(t, err)
			assert.Less(t, float64(seconds), timeout.Seconds(),
				"the helper must exit before the caller context expires for %s", timeout)
		}
	})

	t.Run("timeout is clamped into the bounded window", func(t *testing.T) {
		tooSmall := buildNetworkProbeCommand(domain.ContainerNetworkProbeRequest{
			Protocol: domain.ProbeProtocolTCP, Port: 1, Timeout: time.Millisecond,
		}, "10.0.0.1")
		assert.Equal(t, "1", tooSmall.args[0])

		tooLarge := buildNetworkProbeCommand(domain.ContainerNetworkProbeRequest{
			Protocol: domain.ProbeProtocolTCP, Port: 1, Timeout: time.Hour,
		}, "10.0.0.1")
		assert.Equal(t, "30", tooLarge.args[0])
	})
}

// TestProbeHelperHostConfig_IsHardened proves the helper can reach its
// network and nothing else: no ports, mounts, volumes, or extra capability.
func TestProbeHelperHostConfig_IsHardened(t *testing.T) {
	config := probeHelperHostConfig("gordon--blog--net")
	require.NotNil(t, config)
	assert.Equal(t, "gordon--blog--net", string(config.NetworkMode))
	assert.True(t, config.ReadonlyRootfs)
	assert.Equal(t, []string{"ALL"}, config.CapDrop)
	assert.Empty(t, config.CapAdd)
	assert.Equal(t, []string{"no-new-privileges:true"}, config.SecurityOpt)
	assert.False(t, config.Privileged)

	assert.NotZero(t, config.Memory)
	require.NotNil(t, config.PidsLimit)
	assert.Positive(t, *config.PidsLimit)
	assert.NotZero(t, config.NanoCPUs)

	assert.Empty(t, config.PortBindings)
	assert.Empty(t, config.Binds)
	assert.Empty(t, config.Mounts)
	assert.Empty(t, config.VolumesFrom)
	assert.Empty(t, config.Devices)
	assert.NotEqual(t, container.NetworkMode("none"), config.NetworkMode, "the helper must reach its target network")
}

// TestProbeHelperConfig_NoSharedNetworks proves the helper is created with
// exactly one network and no aliases.
func TestProbeHelperConfig_NoSharedNetworks(t *testing.T) {
	config := probeHelperHostConfig("gordon--blog--net")
	assert.Empty(t, config.ExtraHosts)
	assert.Empty(t, config.DNS)
	assert.Empty(t, config.Links)
}

func TestParseProbeStatus(t *testing.T) {
	assert.Equal(t, 200, parseProbeStatus("200\n"))
	assert.Equal(t, 404, parseProbeStatus("404"))
	assert.Equal(t, 0, parseProbeStatus(""))
	assert.Equal(t, 0, parseProbeStatus("not-a-status"))
	assert.Equal(t, 0, parseProbeStatus("1234"))
}

func TestTruncateProbeOutput(t *testing.T) {
	assert.Equal(t, "200", truncateProbeOutput("  200\n"))
	assert.Equal(t, 64, len(truncateProbeOutput(strings.Repeat("x", 200))))
}

func TestCappedProbeBuffer(t *testing.T) {
	var buffer cappedProbeBuffer
	input := strings.Repeat("x", 1024)
	written, err := buffer.Write([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, len(input), written)
	assert.Len(t, buffer.String(), 64)
}

func TestHTTPProbeScript_RequiresThreeDigitStatus(t *testing.T) {
	assert.Contains(t, httpProbeScript, "$2 ~ /^[0-9][0-9][0-9]$/")
	assert.NotContains(t, httpProbeScript, "2*|3*")
	assert.Contains(t, httpProbeScript, "head -c 128")
}

func TestProbeCleanupResult_PriorErrorNeverHidesCleanupFailure(t *testing.T) {
	cleanupErr := errors.New("remove failed")
	priorErr := fmt.Errorf("stale: %w", domain.ErrAppStateConflict)
	result, err := probeCleanupResult(zerowrap.Default(), func() error { return cleanupErr }, domain.ContainerNetworkProbeResult{Ready: true}, priorErr)
	assert.False(t, result.Ready)
	require.ErrorIs(t, err, domain.ErrNetworkProbeCleanup)
	require.ErrorIs(t, err, priorErr)
}

func TestProbeDiagnostic(t *testing.T) {
	http := domain.ContainerNetworkProbeRequest{Protocol: domain.ProbeProtocolHTTP}
	assert.Equal(t, "http status 503", probeDiagnostic(http, 503))
	assert.Equal(t, "no http response", probeDiagnostic(http, 0))

	tcp := domain.ContainerNetworkProbeRequest{Protocol: domain.ProbeProtocolTCP}
	assert.Equal(t, "tcp connection refused or timed out", probeDiagnostic(tcp, 0))
}

func TestDefaultNetworkProbeImage_IsDigestPinned(t *testing.T) {
	assert.Contains(t, DefaultNetworkProbeImage, "@sha256:", "the helper image must be pinned by digest")
	assert.True(t, strings.HasPrefix(DefaultNetworkProbeImage, "alpine@"), "the helper needs only busybox shell tools")
}
