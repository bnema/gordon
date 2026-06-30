package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
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

func TestTrafficStatusRejectsArgs(t *testing.T) {
	cmd := newTrafficStatusCmd()
	require.Error(t, cmd.Args(cmd, []string{"extra"}))
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

func TestRunTrafficStatusUsesControlPlane(t *testing.T) {
	status := &dto.TrafficStatusResponse{LastReloadStatus: "ok"}
	cp := &trafficStatusPlane{status: status}

	var buf bytes.Buffer
	require.NoError(t, runTrafficStatus(context.Background(), cp, &buf, true))

	assert.True(t, cp.called)
	assert.Contains(t, buf.String(), `"last_reload_status": "ok"`)
}

func TestRemoteTrafficStatusControlPlaneStillRenders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/traffic/status", r.URL.Path)
		_, err := w.Write([]byte(`{"last_reload_status":"ok","counters":{"active_udp_sessions":3}}`))
		require.NoError(t, err)
	}))
	defer srv.Close()

	cp := NewRemoteControlPlane(remote.NewClient(srv.URL))
	var buf bytes.Buffer
	require.NoError(t, runTrafficStatus(context.Background(), cp, &buf, false))

	output := buf.String()
	assert.Contains(t, output, "Traffic Status")
	assert.Contains(t, output, "Reload")
	assert.Contains(t, output, "ok")
	assert.Contains(t, output, "udp=3")
}

func TestLocalTrafficStatusReturnsActionableDaemonGuidance(t *testing.T) {
	_, err := (&localControlPlane{}).GetTrafficStatus(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "in-process CLI control plane")
	assert.Contains(t, err.Error(), "running Gordon daemon")
	assert.Contains(t, err.Error(), "--remote")
	assert.Contains(t, err.Error(), "GORDON_REMOTE")
}

type trafficStatusPlane struct {
	ControlPlane
	status *dto.TrafficStatusResponse
	called bool
}

func (p *trafficStatusPlane) GetTrafficStatus(context.Context) (*dto.TrafficStatusResponse, error) {
	p.called = true
	return p.status, nil
}
