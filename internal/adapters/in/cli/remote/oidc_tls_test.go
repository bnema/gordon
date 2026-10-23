package remote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
)

// These tests use real httptest servers (sockets, TLS), so they run outside
// synctest and observe requests synchronously.

// countingServer records every request it receives and the credentials
// carried, so a test can prove nothing reached it.
type countingServer struct {
	hits  atomic.Int32
	auths []string
}

func (s *countingServer) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}
}

func assertNoCISecret(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	assert.NotContains(t, err.Error(), testJWT)
	assert.NotContains(t, err.Error(), testCIToken)
}

func TestClientOIDC_PlaintextRemoteRefusedBeforeAnyRequest(t *testing.T) {
	sink := &countingServer{}
	srv := httptest.NewServer(sink.handler(t))
	defer srv.Close()
	require.True(t, strings.HasPrefix(srv.URL, "http://"))

	client := NewClient(srv.URL, WithOIDC(&OIDCSource{static: testJWT}))

	_, err := client.ExchangeCIToken(context.Background(), "repository:web:push,pull")
	require.ErrorIs(t, err, ErrCIInsecureTransport)
	assertNoCISecret(t, err)
	assert.Contains(t, err.Error(), "https")

	_, _, err = client.PromoteApp(context.Background(), "web", dto.AppPromoteRequest{Service: "web", Image: "ci.example.com/web@sha256:" + strings.Repeat("a", 64)})
	require.ErrorIs(t, err, ErrCIInsecureTransport)
	assertNoCISecret(t, err)

	_, err = client.OperationByKey(context.Background(), "web", "key-1")
	require.ErrorIs(t, err, ErrCIInsecureTransport)
	assertNoCISecret(t, err)

	assert.Zero(t, sink.hits.Load(), "no request reached the plaintext remote")
}

func TestClientOIDC_ExchangeRedirectToPlaintextRefused(t *testing.T) {
	sink := &countingServer{}
	plain := httptest.NewServer(sink.handler(t))
	defer plain.Close()

	ciHost := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer ciHost.Close()

	client := NewClient(ciHost.URL, WithHTTPClient(ciHost.Client()), WithOIDC(&OIDCSource{static: testJWT}))
	_, err := client.ExchangeCIToken(context.Background())
	require.ErrorIs(t, err, ErrCIInsecureTransport)
	assertNoCISecret(t, err)
	assert.Contains(t, err.Error(), "redirect")
	assert.Zero(t, sink.hits.Load(), "the identity token never followed the redirect")
}

func TestClientOIDC_CIRouteRedirectToPlaintextRefused(t *testing.T) {
	sink := &countingServer{}
	plain := httptest.NewServer(sink.handler(t))
	defer plain.Close()

	var promotes atomic.Int32
	ciHost := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			_, _ = w.Write([]byte(`{"token":"` + testCIToken + `","expires_in":900}`))
			return
		}
		if r.Method == http.MethodPost {
			promotes.Add(1)
		}
		http.Redirect(w, r, plain.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer ciHost.Close()

	client := NewClient(ciHost.URL, WithHTTPClient(ciHost.Client()), WithOIDC(&OIDCSource{static: testJWT}))

	_, _, err := client.PromoteApp(context.Background(), "web", dto.AppPromoteRequest{Service: "web", Image: "ci.example.com/web@sha256:" + strings.Repeat("a", 64)})
	require.ErrorIs(t, err, ErrCIInsecureTransport)
	assertNoCISecret(t, err)
	var unknown *OutcomeUnknownError
	assert.NotErrorAs(t, err, &unknown, "a refused redirect is a definite failure, not an ambiguous outcome")

	_, err = client.OperationByKey(context.Background(), "web", "key-1")
	require.ErrorIs(t, err, ErrCIInsecureTransport)
	assertNoCISecret(t, err)

	assert.Equal(t, int32(1), promotes.Load(), "the promotion is not retried after a refused redirect")
	assert.Zero(t, sink.hits.Load(), "the ci-access token never followed the redirect")
}

func TestClientOIDC_HTTPSRemoteWorks(t *testing.T) {
	client, host := newOIDCClient(t)
	token, err := client.ExchangeCIToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, testCIToken, token)
	assert.Equal(t, int32(1), host.exchanges.Load())
}
