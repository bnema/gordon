package appapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/domain"
)

// TestErrorResponse_LocksEveryMapping pins the status, code, and hint of
// every app error, including the mappings the admin routes used before
// promotion existed, so no route can silently regress.
func TestErrorResponse_LocksEveryMapping(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
		hint   string
	}{
		// Pre-existing admin mappings.
		{domain.ErrInvalidAppSpec, 400, "invalid-manifest", ""},
		{domain.ErrBindPolicy, 400, "bind-policy-violation", "check the administrative mount authorization"},
		{domain.ErrDevicePolicy, 400, "device-policy-violation", "check the administrative device authorization"},
		{domain.ErrCapabilityPolicy, 400, "capability-policy-violation", "check the administrative capability authorization"},
		{domain.ErrRuntimeGuard, 400, "runtime-guard-violation", "check administrative mounts and network settings"},
		{domain.ErrRuntimeUnsupported, 400, "runtime-unsupported", "use an engine with native CDI support"},
		{domain.ErrAppReservationConflict, 409, "reservation-conflict", ""},
		{domain.ErrAppImageUnresolvable, 400, "image-unresolvable", ""},
		{domain.ErrAppSecretMissing, 400, "secret-missing", "set the secret, then redeploy"},
		{domain.ErrAppUnmanagedImageVolume, 400, "unmanaged-image-volume", ""},
		{domain.ErrAppServiceScope, 400, "service-scope-required", "pass --service NAME or --all"},
		{domain.ErrAppNotFound, 404, "app-not-found", "check the app name"},
		{domain.ErrAppRevisionNotFound, 404, "not-found", ""},
		{domain.ErrAppIntentNotFound, 404, "not-found", ""},
		{domain.ErrAppOperationNotFound, 404, "not-found", ""},
		{domain.ErrAppStateConflict, 409, "state-conflict", ""},
		{errors.New("boom"), 500, "internal", ""},
		// Changed: was 500 before the shared mapping.
		{domain.ErrAppImageNotAllowed, 400, "image-not-allowed", ""},
		// Promotion.
		{ErrInvalidRequest, 400, "invalid-request", ""},
		{domain.ErrInvalidPromotionImage, 400, "invalid-image", "use REPOSITORY@sha256:DIGEST from this installation's registry"},
		{domain.ErrPromotionDenied, 403, "promotion-denied", "check the promotion policies"},
		{domain.ErrIdentityProviderUnavailable, 503, "identity-provider-unavailable", "retry later"},
		{domain.ErrAppServiceNotFound, 404, "service-not-found", "check the service name"},
		{domain.ErrManifestNotFound, 404, "manifest-not-found", "push the image before promoting it"},
		{domain.ErrAppBusy, 409, "app-busy", "another operation of this app is running; retry later with a new key"},
		{domain.ErrPromotionUnavailable, 500, "internal", ""},
	}
	for _, tc := range tests {
		t.Run(tc.err.Error(), func(t *testing.T) {
			status, envelope := ErrorResponse(fmt.Errorf("wrapped: %w", tc.err))
			assert.Equal(t, tc.status, status)
			assert.Equal(t, tc.code, envelope.Error)
			assert.Equal(t, tc.hint, envelope.Hint)
			if tc.status == 500 {
				assert.Equal(t, "app operation failed", envelope.Message, "internal causes never leak")
			}
		})
	}
}

func TestRetryAfter(t *testing.T) {
	assert.Equal(t, "5", RetryAfter(domain.ErrAppBusy))
	assert.Equal(t, "30", RetryAfter(domain.ErrIdentityProviderUnavailable))
	assert.Empty(t, RetryAfter(domain.ErrAppStateConflict))
	rec := httptest.NewRecorder()
	WriteError(rec, fmt.Errorf("x: %w", domain.ErrAppBusy))
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "5", rec.Header().Get("Retry-After"))
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestDecodePromoteRequest(t *testing.T) {
	valid := `{"service":"web","image":"web@sha256:abc","expected_revision":"rev-1"}`
	req, err := decode(t, valid)
	require.NoError(t, err)
	assert.Equal(t, "rev-1", req.ExpectedRevision)

	for name, body := range map[string]string{
		"empty":               ``,
		"unknown field":       `{"service":"web","image":"x","extra":1}`,
		"missing image":       `{"service":"web"}`,
		"missing service":     `{"image":"x"}`,
		"trailing object":     valid + `{}`,
		"trailing brace":      valid + `}`,
		"trailing bracket":    valid + `]`,
		"trailing garbage":    valid + ` x`,
		"not an object":       `[]`,
		"non-canonical case":  `{"Service":"web","image":"x"}`,
		"non-canonical image": `{"service":"web","IMAGE":"x"}`,
		"duplicate case":      `{"service":"web","image":"x","Image":"y"}`,
		"oversized":           `{"service":"web","image":"` + strings.Repeat("a", MaxPromoteRequestSize) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decode(t, body)
			require.ErrorIs(t, err, ErrInvalidRequest)
		})
	}
}

func decode(t *testing.T, body string) (dto.AppPromoteRequest, error) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	return DecodePromoteRequest(httptest.NewRecorder(), r)
}

func TestValidIdempotencyKey(t *testing.T) {
	assert.True(t, ValidIdempotencyKey("key-1"))
	assert.True(t, ValidIdempotencyKey(strings.Repeat("k", 128)))
	assert.False(t, ValidIdempotencyKey(""))
	assert.False(t, ValidIdempotencyKey(strings.Repeat("k", 129)))
	assert.False(t, ValidIdempotencyKey("a b"))
	assert.False(t, ValidIdempotencyKey("é"))
}
