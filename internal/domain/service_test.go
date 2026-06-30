package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStandaloneServiceValidateRequiresNameAndImageWhenEnabled(t *testing.T) {
	svc := StandaloneService{Enabled: true, Image: "redis:7"}
	require.ErrorContains(t, svc.Validate(), "service name")

	svc = StandaloneService{Enabled: true, Name: "cache"}
	require.ErrorContains(t, svc.Validate(), "image")
}

func TestStandaloneServiceValidateAllowsDisabledServiceWithoutImage(t *testing.T) {
	svc := StandaloneService{Name: "cache", Enabled: false}
	require.NoError(t, svc.Validate())
}

func TestStandaloneServiceValidatePorts(t *testing.T) {
	valid := StandaloneService{
		Name:    "game",
		Image:   "game:latest",
		Enabled: true,
		Ports: []StandaloneServicePort{
			{Name: "game", Container: 28015, Protocol: NetworkProtocolUDP, Publish: "127.0.0.1:38015"},
			{Name: "rcon", Container: 28016, Protocol: NetworkProtocolTCP, Publish: "127.0.0.1:38016", Private: true},
		},
	}
	require.NoError(t, valid.Validate())

	duplicate := valid
	duplicate.Ports = append(duplicate.Ports, StandaloneServicePort{Name: "game", Container: 28017, Protocol: NetworkProtocolUDP})
	require.ErrorContains(t, duplicate.Validate(), "duplicate port")

	badProtocol := valid
	badProtocol.Ports = []StandaloneServicePort{{Name: "game", Container: 28015, Protocol: NetworkProtocol("sctp")}}
	require.ErrorContains(t, badProtocol.Validate(), "protocol")

	badPublish := valid
	badPublish.Ports = []StandaloneServicePort{{Name: "game", Container: 28015, Protocol: NetworkProtocolUDP, Publish: "127.0.0.1:not-a-port"}}
	require.ErrorContains(t, badPublish.Validate(), "valid port")

	conflictingVisibility := valid
	conflictingVisibility.Ports = []StandaloneServicePort{{Name: "rcon", Container: 28016, Protocol: NetworkProtocolTCP, Private: true, Public: true}}
	require.ErrorContains(t, conflictingVisibility.Validate(), "cannot be both private and public")
}

func TestStandaloneServiceValidateVolumesAndEnvFile(t *testing.T) {
	svc := StandaloneService{Name: "cache", Image: "redis:7", Enabled: true}
	require.NoError(t, svc.Validate(), "volumes may be omitted")

	svc.Volumes = []StandaloneServiceVolume{{Source: "cache-data", Target: "var/lib/redis"}}
	require.ErrorContains(t, svc.Validate(), "absolute")

	svc.Volumes = nil
	svc.EnvFile = "   "
	require.ErrorContains(t, svc.Validate(), "env_file")
}

func TestStandaloneServiceValidateReadiness(t *testing.T) {
	svc := StandaloneService{Name: "game", Image: "game:latest", Enabled: true}
	for _, readinessType := range []string{"", "none", "tcp", "log"} {
		t.Run(readinessType, func(t *testing.T) {
			candidate := svc
			candidate.Readiness = StandaloneServiceReadiness{Type: readinessType, Path: "/tmp/server.log", Contains: "ready", Timeout: time.Minute}
			require.NoError(t, candidate.Validate())
		})
	}

	svc.Readiness = StandaloneServiceReadiness{Type: "udp"}
	require.ErrorContains(t, svc.Validate(), "readiness")

	for _, timeout := range []time.Duration{0, -time.Second} {
		svc.Readiness = StandaloneServiceReadiness{Type: "tcp", Timeout: timeout, TimeoutSet: true}
		require.ErrorContains(t, svc.Validate(), "readiness timeout")
	}

	svc.Readiness = StandaloneServiceReadiness{Type: "log", Path: "/tmp/server.log"}
	require.ErrorContains(t, svc.Validate(), "contains")

	svc.Readiness = StandaloneServiceReadiness{Type: "log", Contains: "ready"}
	require.ErrorContains(t, svc.Validate(), "path")
}

func TestStandaloneServiceWithDefaultsCleanup(t *testing.T) {
	svc := StandaloneService{Name: "game", Image: "game:latest", Enabled: true}.WithDefaults()
	require.True(t, svc.Cleanup.PreserveVolumes)
	require.True(t, svc.Cleanup.RemoveContainer)
	require.NoError(t, svc.Validate())
}
