package acme

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// challengeSvc is a simple map-backed implementation of ChallengeService for testing.
type challengeSvc map[string]string

func (s challengeSvc) GetHTTP01Challenge(_ context.Context, token string) (string, bool) {
	keyAuth, ok := s[token]
	return keyAuth, ok
}

func newTestServer(t *testing.T, svc ChallengeService) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(NewHandler(svc))
	t.Cleanup(server.Close)
	return server
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

func TestHandlerServesGETChallenge(t *testing.T) {
	server := newTestServer(t, challengeSvc{"abc": "key-auth"})

	resp, err := http.Get(server.URL + "/.well-known/acme-challenge/abc")
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/plain; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "key-auth", readBody(t, resp))
}

func TestHandlerServesHEADChallenge(t *testing.T) {
	server := newTestServer(t, challengeSvc{"abc": "key-auth"})

	resp, err := http.Head(server.URL + "/.well-known/acme-challenge/abc")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/plain; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
}

func TestHandlerRejectsUnsafeToken(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "path traversal", path: "/.well-known/acme-challenge/../secret"},
		{name: "slash in token", path: "/.well-known/acme-challenge/foo/bar"},
		{name: "empty token", path: "/.well-known/acme-challenge/"},
		{name: "backslash in token", path: "/.well-known/acme-challenge/foo%5Cbar"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t, challengeSvc{"abc": "key-auth"})
			resp, err := http.Get(server.URL + tt.path)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusNotFound, resp.StatusCode, "path: %s", tt.path)
		})
	}
}

func TestHandlerRejectsPOST(t *testing.T) {
	server := newTestServer(t, challengeSvc{"abc": "key-auth"})

	resp, err := http.Post(server.URL+"/.well-known/acme-challenge/abc", "text/plain", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	assert.Equal(t, "GET, HEAD", resp.Header.Get("Allow"))
}

func TestHandlerRejectsMissingPrefix(t *testing.T) {
	server := newTestServer(t, challengeSvc{"abc": "key-auth"})

	resp, err := http.Get(server.URL + "/some-other-path")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandlerRejectsUnknownToken(t *testing.T) {
	server := newTestServer(t, challengeSvc{"abc": "key-auth"})

	resp, err := http.Get(server.URL + "/.well-known/acme-challenge/unknown")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandlerRejectsNilService(t *testing.T) {
	server := newTestServer(t, nil)

	resp, err := http.Get(server.URL + "/.well-known/acme-challenge/abc")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
