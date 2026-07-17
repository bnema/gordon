package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyStandaloneServiceCommandValidate(t *testing.T) {
	valid := ApplyStandaloneServiceCommand{
		RuntimeCommandIdentity: validStandaloneRuntimeIdentity(),
		Service: StandaloneService{
			Name:    "game",
			Image:   "example/game:latest",
			Enabled: true,
		},
		ResolvedEnv: []string{"TOKEN=resolved-value"},
		ConfigHash:  "config-hash",
	}

	t.Run("valid", func(t *testing.T) {
		require.NoError(t, valid.Validate())
	})

	t.Run("missing identity", func(t *testing.T) {
		command := valid
		command.RuntimeCommandIdentity = RuntimeCommandIdentity{}

		require.ErrorIs(t, command.Validate(), ErrInvalidRuntimeCommand)
	})

	t.Run("disabled service", func(t *testing.T) {
		command := valid
		command.Service.Enabled = false

		require.ErrorIs(t, command.Validate(), ErrInvalidRuntimeCommand)
	})

	t.Run("empty config hash", func(t *testing.T) {
		command := valid
		command.ConfigHash = "  "

		require.ErrorIs(t, command.Validate(), ErrInvalidRuntimeCommand)
	})

	t.Run("raw environment references", func(t *testing.T) {
		command := valid
		command.Service.Env = []string{"TOKEN=raw-value"}

		err := command.Validate()
		require.ErrorIs(t, err, ErrInvalidRuntimeCommand)
		assert.NotContains(t, err.Error(), "raw-value")
	})
}

func TestRemoveStandaloneServiceCommandValidate(t *testing.T) {
	command := RemoveStandaloneServiceCommand{
		RuntimeCommandIdentity: validStandaloneRuntimeIdentity(),
		Name:                   "game",
	}
	require.NoError(t, command.Validate())

	command.Name = "  "
	require.ErrorIs(t, command.Validate(), ErrInvalidRuntimeCommand)
}

func TestStandaloneServiceForRuntimeApplySanitizesEnvironmentReferences(t *testing.T) {
	service := StandaloneService{
		Name:    "game",
		Image:   "example/game:latest",
		Enabled: true,
		Env:     []string{"TOKEN=raw-secret"},
		EnvFile: "/run/secrets/game.env",
		Secrets: []StandaloneServiceSecretRef{{Name: "token", Key: "TOKEN"}},
		Readiness: StandaloneServiceReadiness{
			Type:       StandaloneServiceReadinessLog,
			Path:       "/logs/server.log",
			Contains:   "ready",
			Timeout:    5 * time.Second,
			TimeoutSet: true,
		},
		Cleanup: StandaloneServiceCleanup{RemoveContainer: true},
		Ports: []StandaloneServicePort{{
			Name:         "game",
			Container:    28015,
			Protocol:     NetworkProtocolUDP,
			Publish:      "127.0.0.1:28015",
			Public:       true,
			TrustedCIDRs: []string{"192.0.2.0/24"},
		}},
		Volumes: []StandaloneServiceVolume{{Source: "game-data", Target: "/data", ReadOnly: true}},
	}

	runtimeService := service.ForRuntimeApply()

	assert.Empty(t, runtimeService.Env)
	assert.Empty(t, runtimeService.EnvFile)
	assert.Empty(t, runtimeService.Secrets)
	assert.Equal(t, service.Name, runtimeService.Name)
	assert.Equal(t, service.Image, runtimeService.Image)
	assert.Equal(t, service.Enabled, runtimeService.Enabled)
	assert.Equal(t, service.Readiness, runtimeService.Readiness)
	assert.Equal(t, service.Cleanup, runtimeService.Cleanup)
	assert.Equal(t, service.Ports, runtimeService.Ports)
	assert.Equal(t, service.Volumes, runtimeService.Volumes)

	runtimePayload := fmt.Sprintf("%#v", runtimeService)
	assert.NotContains(t, runtimePayload, "raw-secret")
	assert.NotContains(t, runtimePayload, "/run/secrets/game.env")
	assert.NotContains(t, runtimePayload, "token")

	runtimeService.Ports[0].TrustedCIDRs[0] = "198.51.100.0/24"
	runtimeService.Volumes[0].Source = "other"
	assert.Equal(t, "192.0.2.0/24", service.Ports[0].TrustedCIDRs[0])
	assert.Equal(t, "game-data", service.Volumes[0].Source)
}

func TestRuntimeStandaloneServiceStateCarriesSanitizedStatus(t *testing.T) {
	state := RuntimeStandaloneServiceState{
		Name:          "game",
		ContainerID:   "container-id",
		ContainerName: "gordon-service-game",
		Status:        ContainerStatusRunning,
		ConfigHash:    "config-hash",
	}

	assert.Equal(t, "game", state.Name)
	assert.Equal(t, "container-id", state.ContainerID)
	assert.Equal(t, "gordon-service-game", state.ContainerName)
	assert.Equal(t, ContainerStatusRunning, state.Status)
	assert.Equal(t, "config-hash", state.ConfigHash)
}

func validStandaloneRuntimeIdentity() RuntimeCommandIdentity {
	return RuntimeCommandIdentity{
		ID:                RuntimeCommandID("standalone-command-1"),
		IdempotencyKey:    "control-1:standalone:game:1",
		Generation:        1,
		SourceComponentID: "control-1",
	}
}

func TestStandaloneRuntimeValidationUsesInvalidRuntimeCommand(t *testing.T) {
	err := ApplyStandaloneServiceCommand{}.Validate()
	assert.True(t, errors.Is(err, ErrInvalidRuntimeCommand))
}
