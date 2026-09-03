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

func TestStandaloneServiceValidateRejectsPaddedNames(t *testing.T) {
	svc := StandaloneService{Name: " cache ", Image: "redis:7", Enabled: true}
	require.ErrorContains(t, svc.Validate(), "leading or trailing whitespace")

	svc = StandaloneService{
		Name:    "cache",
		Image:   "redis:7",
		Enabled: true,
		Ports:   []StandaloneServicePort{{Name: " admin ", Container: 6379, Protocol: ServicePortProtocolTCP}},
	}
	require.ErrorContains(t, svc.Validate(), "leading or trailing whitespace")
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
			{Name: "game", Container: 28015, Protocol: ServicePortProtocolUDP, Publish: "127.0.0.1:38015"},
			{Name: "rcon", Container: 28016, Protocol: ServicePortProtocolTCP, Publish: "127.0.0.1:38016", Private: true},
		},
	}
	require.NoError(t, valid.Validate())

	duplicate := valid
	duplicate.Ports = append(duplicate.Ports, StandaloneServicePort{Name: "game", Container: 28017, Protocol: ServicePortProtocolUDP})
	require.ErrorContains(t, duplicate.Validate(), "duplicate port")

	badProtocol := valid
	badProtocol.Ports = []StandaloneServicePort{{Name: "game", Container: 28015, Protocol: ServicePortProtocol("sctp")}}
	require.ErrorContains(t, badProtocol.Validate(), "protocol")

	badPublish := valid
	badPublish.Ports = []StandaloneServicePort{{Name: "game", Container: 28015, Protocol: ServicePortProtocolUDP, Publish: "127.0.0.1:not-a-port"}}
	require.ErrorContains(t, badPublish.Validate(), "valid port")

	conflictingVisibility := valid
	conflictingVisibility.Ports = []StandaloneServicePort{{Name: "rcon", Container: 28016, Protocol: ServicePortProtocolTCP, Private: true, Public: true}}
	require.ErrorContains(t, conflictingVisibility.Validate(), "cannot be both private and public")

	privateNonLoopback := valid
	privateNonLoopback.Ports = []StandaloneServicePort{{Name: "rcon", Container: 28016, Protocol: ServicePortProtocolTCP, Private: true, Publish: "0.0.0.0:28016"}}
	require.ErrorContains(t, privateNonLoopback.Validate(), "must be loopback")
}

func TestStandaloneServiceValidateVolumesAndEnvFile(t *testing.T) {
	svc := StandaloneService{Name: "cache", Image: "redis:7", Enabled: true}
	require.NoError(t, svc.Validate(), "volumes may be omitted")

	svc.Volumes = []StandaloneServiceVolume{{Source: "cache-data", Target: "var/lib/redis"}}
	require.ErrorContains(t, svc.Validate(), "absolute")

	svc.Volumes = []StandaloneServiceVolume{{Source: "cache-data", Target: `C:\\data`}}
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

	svc = StandaloneService{Name: "game", Image: "game:latest", Enabled: true, Cleanup: StandaloneServiceCleanup{RemoveContainer: true}}.WithDefaults()
	require.False(t, svc.Cleanup.PreserveVolumes)
	require.True(t, svc.Cleanup.RemoveContainer)

	svc = StandaloneService{Name: "game", Image: "game:latest", Enabled: true, Cleanup: StandaloneServiceCleanup{PreserveVolumes: true}}.WithDefaults()
	require.True(t, svc.Cleanup.PreserveVolumes)
	require.False(t, svc.Cleanup.RemoveContainer)
}

func TestStandaloneServiceValidateRejectsDuplicateVolumeTargets(t *testing.T) {
	svc := StandaloneService{
		Name:    "cache",
		Image:   "redis:7",
		Enabled: true,
		Volumes: []StandaloneServiceVolume{
			{Source: "cache-data", Target: "/data"},
			{Source: "cache-other", Target: "/data"},
		},
	}
	require.ErrorContains(t, svc.Validate(), "duplicate volume target")
}

func TestStandaloneServiceValidateNestedContainerConfiguration(t *testing.T) {
	base := StandaloneService{
		Name:    "app",
		Enabled: true,
		Containers: []StandaloneServiceContainer{{
			Name:  "web",
			Image: "app:latest",
		}},
	}

	tests := []struct {
		name        string
		configure   func(*StandaloneServiceContainer)
		wantErrText string
	}{
		{name: "blank env file", configure: func(c *StandaloneServiceContainer) { c.EnvFile = "   " }, wantErrText: "env_file"},
		{name: "missing secret name", configure: func(c *StandaloneServiceContainer) { c.Secrets = []StandaloneServiceSecretRef{{Key: "TOKEN"}} }, wantErrText: "secret"},
		{name: "relative volume target", configure: func(c *StandaloneServiceContainer) { c.Volumes = []StandaloneServiceVolume{{Target: "data"}} }, wantErrText: "absolute"},
		{name: "log readiness missing path", configure: func(c *StandaloneServiceContainer) {
			c.Readiness = StandaloneServiceReadiness{Type: StandaloneServiceReadinessLog, Contains: "ready"}
		}, wantErrText: "path"},
		{name: "log readiness missing contains", configure: func(c *StandaloneServiceContainer) {
			c.Readiness = StandaloneServiceReadiness{Type: StandaloneServiceReadinessLog, Path: "/tmp/app.log"}
		}, wantErrText: "contains"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := base
			svc.Containers = append([]StandaloneServiceContainer(nil), base.Containers...)
			tt.configure(&svc.Containers[0])
			require.ErrorContains(t, svc.Validate(), tt.wantErrText)
		})
	}
}
