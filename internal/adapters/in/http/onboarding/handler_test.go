package onboarding_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bnema/gordon/internal/adapters/in/http/onboarding"
)

func TestHandler_CACert(t *testing.T) {
	rootPEM := []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n")
	rootDER := []byte("fakeDER")
	h := onboarding.NewHandler(rootPEM, rootDER, "Gordon Test CA", nil)

	req := httptest.NewRequest("GET", "/ca.crt", nil)
	w := httptest.NewRecorder()
	h.ServeCACert(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/x-x509-ca-cert", w.Header().Get("Content-Type"))
	assert.Equal(t, rootPEM, w.Body.Bytes())
}

func TestHandler_Mobileconfig(t *testing.T) {
	rootPEM := []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n")
	rootDER := []byte("fakeDER")
	h := onboarding.NewHandler(rootPEM, rootDER, "Gordon Test CA", nil)

	req := httptest.NewRequest("GET", "/ca.mobileconfig", nil)
	w := httptest.NewRecorder()
	h.ServeMobileconfig(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/x-apple-aspen-config", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), "com.apple.security.root")
}

func TestHandler_OnboardingPage(t *testing.T) {
	rootPEM := []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n")
	rootDER := []byte("fakeDER")
	h := onboarding.NewHandler(rootPEM, rootDER, "Gordon Test CA", nil)

	req := httptest.NewRequest("GET", "/ca", nil)
	w := httptest.NewRecorder()
	h.ServeOnboardingPage(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))
	body := w.Body.String()
	assert.Contains(t, body, "Gordon")
	assert.Contains(t, body, "/ca.crt")
	assert.Contains(t, body, "/ca.mobileconfig")
}
