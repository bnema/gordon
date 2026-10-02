package admin

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
)

func TestHandler_CA(t *testing.T) {
	expiry := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	caSvc := inmocks.NewMockCAInfoService(t)
	caSvc.EXPECT().RootCommonName().Return("Gordon Root")
	caSvc.EXPECT().RootFingerprint().Return("AB:CD")
	caSvc.EXPECT().IntermediateExpiresAt().Return(expiry)
	caSvc.EXPECT().RootCertificate().Return([]byte("-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----\n"))
	handler := newTestHandler(t, func(d *HandlerDeps) { d.CASvc = caSvc })
	server := newScopedTestServer(t, handler, "admin:status:read")

	resp, err := http.Get(server.URL + "/admin/ca")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body dto.CAResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "Gordon Root", body.RootCN)
	assert.Equal(t, "AB:CD", body.Fingerprint)
	assert.True(t, expiry.Equal(body.IntermediateExpiry))
	assert.Contains(t, body.RootPEM, "BEGIN CERTIFICATE")
}

func TestHandler_CARequiresStatusRead(t *testing.T) {
	caSvc := inmocks.NewMockCAInfoService(t)
	handler := newTestHandler(t, func(d *HandlerDeps) { d.CASvc = caSvc })
	server := newScopedTestServer(t, handler, "admin:config:read")

	resp, err := http.Get(server.URL + "/admin/ca")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestHandler_CARejectsNonGet(t *testing.T) {
	handler := newTestHandler(t, func(d *HandlerDeps) { d.CASvc = inmocks.NewMockCAInfoService(t) })
	server := newScopedTestServer(t, handler, "admin:status:read")

	resp, err := http.Post(server.URL+"/admin/ca", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestHandler_CADisabledReturnsNotFound(t *testing.T) {
	handler := newTestHandler(t, func(d *HandlerDeps) { d.CASvc = nil })
	server := newScopedTestServer(t, handler, "admin:status:read")

	resp, err := http.Get(server.URL + "/admin/ca")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	var body dto.ErrorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(t, body.Error, "internal TLS is disabled")
}
