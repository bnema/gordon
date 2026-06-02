package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
