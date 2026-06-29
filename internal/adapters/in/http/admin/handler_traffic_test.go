package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/domain"
)

type fakeTrafficStatusService struct{ status domain.TrafficStatus }

func (f fakeTrafficStatusService) Status() domain.TrafficStatus { return f.status }

func TestHandler_TrafficStatus(t *testing.T) {
	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.TrafficSvc = fakeTrafficStatusService{status: domain.TrafficStatus{
			LastReloadStatus: "ok",
			EntryPoints: []domain.EntryPointStatus{{
				Name: "postgres", Address: "127.0.0.1:5432", Protocol: domain.EntryPointProtocolTCP,
				Active: true, ActiveTCPConnections: 2, TotalAccepted: 3, BytesIn: 11, BytesOut: 22,
			}},
			Counters: domain.TrafficCounters{ActiveTCPConnections: 2, TotalAccepted: 3, BytesIn: 11, BytesOut: 22},
		}}
	})
	server := newScopedTestServer(t, handler, "admin:status:read")

	resp, err := http.Get(server.URL + "/admin/traffic/status")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body dto.TrafficStatusResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "ok", body.LastReloadStatus)
	require.Len(t, body.EntryPoints, 1)
	assert.Equal(t, "postgres", body.EntryPoints[0].Name)
	assert.Equal(t, int64(2), body.Counters.ActiveTCPConnections)
	assert.Equal(t, int64(22), body.Counters.BytesOut)
}

func TestHandler_TrafficStatusRequiresStatusRead(t *testing.T) {
	handler := newTestHandler(t, func(d *HandlerDeps) { d.TrafficSvc = fakeTrafficStatusService{} })
	server := newScopedTestServer(t, handler, "admin:config:read")

	resp, err := http.Get(server.URL + "/admin/traffic/status")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}
