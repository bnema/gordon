package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestLogger_RedactsSensitiveReferers(t *testing.T) {
	tests := []struct {
		name        string
		referer     string
		wantReferer string
	}{
		{
			name:        "query params",
			referer:     "https://idp.example.com/callback?code=secret-code&state=ok&token=secret-token",
			wantReferer: "https://idp.example.com/callback?code=[REDACTED]&state=ok&token=[REDACTED]",
		},
		{
			name:        "fragment removed",
			referer:     "https://idp.example.com/callback?state=ok#access_token=secret-token",
			wantReferer: "https://idp.example.com/callback?state=ok",
		},
		{
			name:        "userinfo removed",
			referer:     "https://user:secret-password@idp.example.com/callback?code=secret-code",
			wantReferer: "https://idp.example.com/callback?code=[REDACTED]",
		},
		{
			name:        "malformed query key redacted conservatively",
			referer:     "https://idp.example.com/callback?token%ZZ=secret-token&state=ok",
			wantReferer: "https://idp.example.com/callback?token%ZZ=[REDACTED]&state=ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			log := zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: &logs})

			handler := RequestLogger(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))

			req := httptest.NewRequest(http.MethodGet, "/target?token=direct-secret", nil)
			req.Header.Set("Referer", tt.referer)
			rw := httptest.NewRecorder()

			handler.ServeHTTP(rw, req)

			logOutput := logs.String()
			require.NotEmpty(t, logOutput)
			var entry map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
			assert.Equal(t, "token=[REDACTED]", entry["query"])
			assert.Equal(t, tt.wantReferer, entry["referer"])
			assert.NotContains(t, logOutput, "direct-secret")
			assert.NotContains(t, logOutput, "secret-code")
			assert.NotContains(t, logOutput, "secret-token")
			assert.NotContains(t, logOutput, "secret-password")
		})
	}
}

func TestPanicRecovery_RethrowsReverseProxyAbort(t *testing.T) {
	log := testLogger()

	handler := PanicRecovery(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	rw := httptest.NewRecorder()

	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		handler.ServeHTTP(rw, req)
	})
	assert.NotEqual(t, http.StatusInternalServerError, rw.Code)
}
