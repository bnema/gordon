package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/domain"
)

func TestTrafficCommandExists(t *testing.T) {
	cmd := NewRootCmd()
	traffic, _, err := cmd.Find([]string{"traffic"})
	require.NoError(t, err)
	require.NotNil(t, traffic)
	assert.Equal(t, "traffic", traffic.Name())

	status, _, err := cmd.Find([]string{"traffic", "status"})
	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, "status", status.Name())
}

func TestTrafficStatusHumanOutput(t *testing.T) {
	status := &dto.TrafficStatusResponse{
		LastReloadStatus: "ok",
		EntryPoints: []dto.TrafficEntryPointStatus{{
			Name: "postgres", Address: "127.0.0.1:5432", Protocol: domain.EntryPointProtocolTCP,
			Active: true, ActiveTCPConnections: 1, TotalAccepted: 2, BytesIn: 10, BytesOut: 20,
			SmartTCP: dto.SmartTCPCounters{HTTPAccepted: 1, PROXYRefused: 1},
		}},
		Routers: []dto.TrafficRouterStatus{{
			Name: "pg-router", EntryPoint: "postgres", Protocol: domain.RouterProtocolTCP,
			Service: "network_service:postgres:db", Active: true,
		}},
		Services: []dto.TrafficServiceStatus{{
			Name: "network_service:postgres:db", Active: true,
			Backends: []dto.TrafficBackendStatus{{Name: "postgres:db", Host: "127.0.0.1", Port: 5432, Protocol: domain.NetworkProtocolTCP, Active: true}},
		}},
		Counters: dto.TrafficCounters{ActiveTCPConnections: 1, TotalAccepted: 2, BytesIn: 10, BytesOut: 20, SmartTCP: dto.SmartTCPCounters{HTTPAccepted: 1, PROXYRefused: 1}},
	}
	var buf bytes.Buffer
	require.NoError(t, renderTrafficStatus(&buf, status, false))
	output := buf.String()
	assert.Contains(t, output, "Traffic Status")
	assert.Contains(t, output, "postgres")
	assert.Contains(t, output, "127.0.0.1:5432")
	assert.Contains(t, output, "pg-router")
	assert.Contains(t, output, "network_service:postgres:db")
	assert.Contains(t, output, "tcp://127.0.0.1:5432")
	assert.Contains(t, output, "accepted=2")
	assert.Contains(t, output, "smart_tcp")
	assert.Contains(t, output, "http_accepted=1")
	assert.Contains(t, output, "proxy_refused=1")
}

func TestTrafficStatusJSONOutput(t *testing.T) {
	status := &dto.TrafficStatusResponse{LastReloadStatus: "ok", Counters: dto.TrafficCounters{ActiveUDPSessions: 3, SmartTCP: dto.SmartTCPCounters{SniffTimeout: 1}}}
	var buf bytes.Buffer
	require.NoError(t, renderTrafficStatus(&buf, status, true))
	var got dto.TrafficStatusResponse
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "ok", got.LastReloadStatus)
	assert.Equal(t, int64(3), got.Counters.ActiveUDPSessions)
	assert.Equal(t, int64(1), got.Counters.SmartTCP.SniffTimeout)
}
