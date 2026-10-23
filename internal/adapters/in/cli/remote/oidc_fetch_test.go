package remote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOIDCSource_OversizedRunnerResponseIsRejected(t *testing.T) {
	huge := strings.Repeat("x", maxIDTokenResponse+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"value":"` + huge + `"}`))
	}))
	defer srv.Close()

	src := &OIDCSource{requestURL: srv.URL, requestToken: testRequestToken, audience: "aud"}
	jwt, err := src.IDToken(context.Background())
	require.Error(t, err)
	assert.Empty(t, jwt)
	assert.Contains(t, err.Error(), "fetch OIDC ID token")
	assert.NotContains(t, err.Error(), "xxxxxxxx", "no part of the body is echoed")
	assert.NotContains(t, err.Error(), testRequestToken)
}

// TestOIDCSource_FetchTimeoutIsReported uses a real httptest server, so it
// cannot run in a synctest bubble (sockets are not durably blocking). The
// server blocks until the client gives up; the caller's context deadline
// bounds the fetch and a channel releases the handler, so no sleep is
// involved.
func TestOIDCSource_FetchTimeoutIsReported(t *testing.T) {
	release := make(chan struct{})
	arrived := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	src := &OIDCSource{requestURL: srv.URL + "?api-version=2.0", requestToken: testRequestToken, audience: "aud"}
	jwt, err := src.IDToken(ctx)
	<-arrived
	require.Error(t, err)
	assert.Empty(t, jwt)
	assert.Contains(t, err.Error(), "timed out")
	assert.NotContains(t, err.Error(), testRequestToken)
}
