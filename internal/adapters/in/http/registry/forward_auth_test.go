package registry

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
)

func TestEdgeForwardAuthPermitsOnlyValidNonLoopbackForwardingAndStripsCredential(t *testing.T) {
	const token = "edge-registry-forward-credential"
	called := false
	handler := EdgeForwardAuth(token, zerowrap.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		assert.Empty(t, r.Header.Get(edgeForwardAuthHeader), "internal forwarding credential must not reach OCI or auth handlers")
		assert.Equal(t, "Bearer client-token", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, test := range []struct {
		name       string
		remoteAddr string
		token      string
		status     int
	}{
		{name: "authenticated edge", remoteAddr: "10.42.0.8:1234", token: token, status: http.StatusNoContent},
		{name: "missing credential", remoteAddr: "10.42.0.8:1234", status: http.StatusForbidden},
		{name: "wrong credential", remoteAddr: "10.42.0.8:1234", token: "attacker-value", status: http.StatusForbidden},
		{name: "loopback health client", remoteAddr: "127.0.0.1:1234", status: http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			called = false
			req := httptest.NewRequest(http.MethodGet, "http://registry.internal/v2/", nil)
			req.RemoteAddr = test.remoteAddr
			req.Header.Set(edgeForwardAuthHeader, test.token)
			req.Header.Set("Authorization", "Bearer client-token")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, test.status, rec.Code)
			assert.Equal(t, test.status == http.StatusNoContent, called)
		})
	}
}
