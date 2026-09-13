package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// TestContainerNetworkProbeRequest_Validate proves the bounded probe
// request fails closed on every missing or malformed field.
func TestContainerNetworkProbeRequest_Validate(t *testing.T) {
	valid := domain.ContainerNetworkProbeRequest{
		TargetContainerID: "abc123",
		ExpectedStartedAt: time.Now().UTC(),
		Network:           "gordon--app--net",
		Protocol:          domain.ProbeProtocolHTTP,
		Port:              8080,
		Path:              "/healthz",
		Timeout:           time.Second,
	}
	require.NoError(t, valid.Validate())

	tests := []struct {
		name   string
		mutate func(*domain.ContainerNetworkProbeRequest)
	}{
		{"missing target id", func(r *domain.ContainerNetworkProbeRequest) { r.TargetContainerID = "  " }},
		{"missing network", func(r *domain.ContainerNetworkProbeRequest) { r.Network = "" }},
		{"missing execution start", func(r *domain.ContainerNetworkProbeRequest) { r.ExpectedStartedAt = time.Time{} }},
		{"zero port", func(r *domain.ContainerNetworkProbeRequest) { r.Port = 0 }},
		{"port too high", func(r *domain.ContainerNetworkProbeRequest) { r.Port = 70000 }},
		{"zero timeout", func(r *domain.ContainerNetworkProbeRequest) { r.Timeout = 0 }},
		{"unknown protocol", func(r *domain.ContainerNetworkProbeRequest) { r.Protocol = "grpc" }},
		{"http without path", func(r *domain.ContainerNetworkProbeRequest) { r.Path = "" }},
		{"http path not origin form", func(r *domain.ContainerNetworkProbeRequest) { r.Path = "healthz" }},
		{"tcp with path", func(r *domain.ContainerNetworkProbeRequest) {
			r.Protocol = domain.ProbeProtocolTCP
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := valid
			tc.mutate(&request)
			err := request.Validate()
			require.Error(t, err)
			assert.True(t, errors.Is(err, domain.ErrInvalidNetworkProbe), "want ErrInvalidNetworkProbe, got %v", err)
		})
	}
}

// TestContainerNetworkProbeRequest_ValidateTCP proves a pathless TCP probe
// is valid.
func TestContainerNetworkProbeRequest_ValidateTCP(t *testing.T) {
	request := domain.ContainerNetworkProbeRequest{
		TargetContainerID: "abc123",
		ExpectedStartedAt: time.Now().UTC(),
		Network:           "gordon--app--net",
		Protocol:          domain.ProbeProtocolTCP,
		Port:              5432,
		Timeout:           time.Second,
	}
	require.NoError(t, request.Validate())
}
