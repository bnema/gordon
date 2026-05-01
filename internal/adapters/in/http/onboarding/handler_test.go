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

const (
	testFingerprint = "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99"
	testHTTPPort    = 8088
	testTLSPort     = 8443
)

func newTestHandler() (*onboarding.Handler, []byte, []byte) {
	rootPEM := []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n")
	mobileconfig := []byte("<plist>com.apple.security.root</plist>")

	h := onboarding.NewHandler(rootPEM, mobileconfig, testFingerprint, testHTTPPort, testTLSPort)
	return h, rootPEM, mobileconfig
}

func newTestServer(t *testing.T) (*httptest.Server, []byte, []byte) {
	t.Helper()
	h, rootPEM, mobileconfig := newTestHandler()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/gordon/{$}", h.ServeOnboardingPage)
	mux.HandleFunc("HEAD /.well-known/gordon/{$}", h.ServeOnboardingPage)
	mux.HandleFunc("GET /.well-known/gordon/ca", h.ServeOnboardingPage)
	mux.HandleFunc("HEAD /.well-known/gordon/ca", h.ServeOnboardingPage)
	mux.HandleFunc("GET /.well-known/gordon/ca.crt", h.ServeCACert)
	mux.HandleFunc("HEAD /.well-known/gordon/ca.crt", h.ServeCACert)
	mux.HandleFunc("GET /.well-known/gordon/ca.mobileconfig", h.ServeMobileconfig)
	mux.HandleFunc("HEAD /.well-known/gordon/ca.mobileconfig", h.ServeMobileconfig)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, rootPEM, mobileconfig
}

// getOnboardingBody issues a GET /.well-known/gordon/ca with a custom Host header and returns the body.
func getOnboardingBody(t *testing.T, srv *httptest.Server, host string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/.well-known/gordon/ca", nil)
	require.NoError(t, err)
	req.Host = host

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

func TestHandler_CACert(t *testing.T) {
	srv, rootPEM, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/.well-known/gordon/ca.crt")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/x-x509-ca-cert", resp.Header.Get("Content-Type"))
	assert.Equal(t, rootPEM, body)
}

func TestHandler_Mobileconfig(t *testing.T) {
	srv, _, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/.well-known/gordon/ca.mobileconfig")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/x-apple-aspen-config", resp.Header.Get("Content-Type"))
	assert.Contains(t, string(body), "com.apple.security.root")
}

func TestHandler_OnboardingPage(t *testing.T) {
	srv, _, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/.well-known/gordon/")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Contains(t, string(body), "Gordon")
	assert.Contains(t, string(body), "/.well-known/gordon/ca.crt")
	assert.Contains(t, string(body), "/.well-known/gordon/ca.mobileconfig")
}

func TestHandler_OnboardingPage_DefaultHTTPSURLOmitsInternalTLSPort(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := getOnboardingBody(t, srv, "o2.bnema.dev")

	assert.Contains(t, body, "https://o2.bnema.dev/")
	assert.NotContains(t, body, ":8443")
}

func TestHandler_OnboardingPage_ExplicitHTTPPortMapsToTLSPort(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := getOnboardingBody(t, srv, "o2.bnema.dev:8088")

	assert.Contains(t, body, "https://o2.bnema.dev:8443/")
}

func TestHandler_OnboardingPage_ExplicitNonHTTPPortIsPreserved(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := getOnboardingBody(t, srv, "o2.bnema.dev:9999")

	assert.Contains(t, body, "https://o2.bnema.dev:9999/")
}

func TestHandler_OnboardingPage_IPHostHidesGoToSiteLink(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := getOnboardingBody(t, srv, "100.83.240.53")

	assert.NotContains(t, body, "Go to site")
}

func TestHandler_OnboardingPage_IncludesFingerprintAndClientImportCopy(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := getOnboardingBody(t, srv, "o2.bnema.dev")

	// Fingerprint must be visible
	assert.Contains(t, body, testFingerprint)

	// Client-import focused: Firefox/Zen guidance present
	assert.Contains(t, body, "Firefox")
	assert.Contains(t, body, "Zen")

	// Should NOT suggest gordon ca install as the primary action
	assert.NotContains(t, body, "sudo gordon ca install")
}

func TestHandler_OnboardingPage_GoToSiteLinkIsPlainLink(t *testing.T) {
	srv, _, _ := newTestServer(t)

	body := getOnboardingBody(t, srv, "o2.bnema.dev")

	assert.Contains(t, body, `<a href="https://o2.bnema.dev/">Go to site &#x2192;</a>`)
	assert.NotContains(t, body, "onclick=")
	assert.NotContains(t, body, "document.cookie")
}

func TestHandler_OnboardingPage_HEAD(t *testing.T) {
	srv, _, _ := newTestServer(t)

	req, err := http.NewRequest(http.MethodHead, srv.URL+"/.well-known/gordon/ca", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Empty(t, body)
}

func TestHandler_CACert_HEAD(t *testing.T) {
	srv, _, _ := newTestServer(t)

	req, err := http.NewRequest(http.MethodHead, srv.URL+"/.well-known/gordon/ca.crt", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/x-x509-ca-cert", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Empty(t, body)
}

func TestHandler_Mobileconfig_HEAD(t *testing.T) {
	srv, _, _ := newTestServer(t)

	req, err := http.NewRequest(http.MethodHead, srv.URL+"/.well-known/gordon/ca.mobileconfig", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/x-apple-aspen-config", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Empty(t, body)
}
