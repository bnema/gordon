package acme

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// challengeSvc is a simple map-backed implementation of ChallengeService for testing.
type challengeSvc map[string]string

func (s challengeSvc) GetHTTP01Challenge(_ context.Context, token string) (string, bool) {
	keyAuth, ok := s[token]
	return keyAuth, ok
}

func TestHandlerServesGETChallenge(t *testing.T) {
	svc := challengeSvc{"abc": "key-auth"}
	h := NewHandler(svc)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/abc", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/plain; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "key-auth", rec.Body.String())
}

func TestHandlerServesHEADChallenge(t *testing.T) {
	svc := challengeSvc{"abc": "key-auth"}
	h := NewHandler(svc)

	req := httptest.NewRequest(http.MethodHead, "/.well-known/acme-challenge/abc", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/plain; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.Empty(t, rec.Body.String())
}

func TestHandlerRejectsUnsafeToken(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "path traversal", path: "/.well-known/acme-challenge/../secret"},
		{name: "slash in token", path: "/.well-known/acme-challenge/foo/bar"},
		{name: "empty token", path: "/.well-known/acme-challenge/"},
		{name: "backslash in token", path: "/.well-known/acme-challenge/foo\\bar"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(challengeSvc{"abc": "key-auth"})

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusNotFound, rec.Code, "path: %s", tt.path)
		})
	}
}

func TestHandlerRejectsPOST(t *testing.T) {
	svc := challengeSvc{"abc": "key-auth"}
	h := NewHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/.well-known/acme-challenge/abc", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestHandlerRejectsMissingPrefix(t *testing.T) {
	h := NewHandler(challengeSvc{"abc": "key-auth"})

	req := httptest.NewRequest(http.MethodGet, "/some-other-path", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandlerRejectsUnknownToken(t *testing.T) {
	h := NewHandler(challengeSvc{"abc": "key-auth"})

	req := httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/unknown", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandlerRejectsNilService(t *testing.T) {
	h := NewHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/abc", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
