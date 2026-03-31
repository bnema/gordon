package onboarding_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/http/onboarding"
)

func newTestServer(t *testing.T, tlsPort int) (*httptest.Server, []byte, []byte) {
	t.Helper()
	rootPEM := []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n")
	mobileconfig := []byte("<plist>com.apple.security.root</plist>")

	h := onboarding.NewHandler(rootPEM, mobileconfig, tlsPort)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ca", h.ServeOnboardingPage)
	mux.HandleFunc("GET /ca.crt", h.ServeCACert)
	mux.HandleFunc("GET /ca.mobileconfig", h.ServeMobileconfig)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, rootPEM, mobileconfig
}

func TestHandler_CACert(t *testing.T) {
	srv, rootPEM, _ := newTestServer(t, 443)

	resp, err := http.Get(srv.URL + "/ca.crt")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/x-x509-ca-cert", resp.Header.Get("Content-Type"))
	assert.Equal(t, rootPEM, body)
}

func TestHandler_Mobileconfig(t *testing.T) {
	srv, _, _ := newTestServer(t, 443)

	resp, err := http.Get(srv.URL + "/ca.mobileconfig")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/x-apple-aspen-config", resp.Header.Get("Content-Type"))
	assert.Contains(t, string(body), "com.apple.security.root")
}

func TestHandler_OnboardingPage(t *testing.T) {
	srv, _, _ := newTestServer(t, 443)

	resp, err := http.Get(srv.URL + "/ca")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Contains(t, string(body), "Gordon")
	assert.Contains(t, string(body), "/ca.crt")
	assert.Contains(t, string(body), "/ca.mobileconfig")
}
