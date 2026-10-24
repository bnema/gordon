// Package appapi holds the HTTP mapping of app mutations that more than one
// driving adapter serves: the admin API and the CI host both decode
// set-image requests, call the same in.AppService port, and map its results
// to the same status codes and DTOs.
package appapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
)

// MaxSetImageRequestSize caps a set-image request body.
const MaxSetImageRequestSize = 4 << 10

// ErrInvalidRequest marks a malformed set-image request body.
var ErrInvalidRequest = errors.New("invalid set-image request")

// setImageFields are the canonical JSON field names of a set-image body.
var setImageFields = map[string]struct{}{"service": {}, "image": {}, "expected_revision": {}}

// DecodeSetImageRequest strictly decodes one size-capped set-image body:
// unknown or non-canonically cased fields, trailing data (including a stray
// closing brace or bracket), and a missing service or image are refused.
func DecodeSetImageRequest(w http.ResponseWriter, r *http.Request) (dto.AppSetImageRequest, error) {
	var req dto.AppSetImageRequest
	if r.Body == nil {
		return req, fmt.Errorf("%w: body required", ErrInvalidRequest)
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxSetImageRequestSize))
	if err != nil {
		return req, fmt.Errorf("%w: body unreadable or too large", ErrInvalidRequest)
	}
	if err := requireCanonicalFields(raw); err != nil {
		return req, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return req, fmt.Errorf("%w: invalid JSON", ErrInvalidRequest)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return req, fmt.Errorf("%w: trailing data", ErrInvalidRequest)
	}
	if strings.TrimSpace(req.Service) == "" || strings.TrimSpace(req.Image) == "" {
		return req, fmt.Errorf("%w: service and image are required", ErrInvalidRequest)
	}
	return req, nil
}

// requireCanonicalFields refuses keys that encoding/json would match
// case-insensitively onto a known field.
func requireCanonicalFields(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&fields); err != nil {
		return fmt.Errorf("%w: body must be a JSON object", ErrInvalidRequest)
	}
	for key := range fields {
		if _, ok := setImageFields[key]; !ok {
			return fmt.Errorf("%w: unknown field %q", ErrInvalidRequest, key)
		}
	}
	return nil
}

// ValidIdempotencyKey accepts URL-unreserved tokens of at most 128 bytes.
// Dot segments are excluded so a key survives path routing unchanged.
func ValidIdempotencyKey(key string) bool {
	if key == "" || key == "." || key == ".." || len(key) > 128 {
		return false
	}
	for _, char := range key {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.~", char) {
			return false
		}
	}
	return true
}

// SetImageInput builds the port input from a decoded request and the caller
// identity the driving adapter established.
func SetImageInput(app, key string, req dto.AppSetImageRequest, principal domain.WorkloadPrincipal, localOwner bool) in.SetImageInput {
	return in.SetImageInput{
		App: app, Service: req.Service, Image: req.Image, ExpectedRevision: req.ExpectedRevision,
		IdempotencyKey: key, Principal: principal, LocalOwner: localOwner,
	}
}

// SetImageOutcome classifies a SetImage result. Respond is false when no
// journal exists: the request was refused before any work and is answered
// with ErrorResponse. Otherwise the journal is the body and Status is 409
// for a replay that failed or is still unfinished, 202 for a newly owned
// running operation, and 200 for a terminal success.
func SetImageOutcome(op *domain.AppOperation, err error) (status int, respond bool) {
	if op == nil {
		return 0, false
	}
	switch {
	case err != nil:
		return http.StatusConflict, true
	case !op.Terminal():
		return http.StatusAccepted, true
	default:
		return http.StatusOK, true
	}
}

// errorMapping maps one domain sentinel to its HTTP status and stable error
// code. The first matching entry wins, so order matters.
type errorMapping struct {
	err    error
	status int
	code   string
	hint   string
}

var errorMappings = []errorMapping{
	{ErrInvalidRequest, http.StatusBadRequest, "invalid-request", ""},
	{domain.ErrInvalidAppSpec, http.StatusBadRequest, "invalid-manifest", ""},
	{domain.ErrBindPolicy, http.StatusBadRequest, "bind-policy-violation", "check the administrative mount authorization"},
	{domain.ErrDevicePolicy, http.StatusBadRequest, "device-policy-violation", "check the administrative device authorization"},
	{domain.ErrCapabilityPolicy, http.StatusBadRequest, "capability-policy-violation", "check the administrative capability authorization"},
	{domain.ErrRuntimeGuard, http.StatusBadRequest, "runtime-guard-violation", "check administrative mounts and network settings"},
	{domain.ErrRuntimeUnsupported, http.StatusBadRequest, "runtime-unsupported", "use an engine with native CDI support"},
	{domain.ErrAppReservationConflict, http.StatusConflict, "reservation-conflict", ""},
	{domain.ErrAppImageUnresolvable, http.StatusBadRequest, "image-unresolvable", ""},
	{domain.ErrAppImageNotAllowed, http.StatusBadRequest, "image-not-allowed", ""},
	{domain.ErrAppSecretMissing, http.StatusBadRequest, "secret-missing", "set the secret, then redeploy"},
	{domain.ErrAppUnmanagedImageVolume, http.StatusBadRequest, "unmanaged-image-volume", ""},
	{domain.ErrAppServiceScope, http.StatusBadRequest, "service-scope-required", "pass --service NAME or --all"},
	{domain.ErrInvalidImageReference, http.StatusBadRequest, "invalid-image", "use REPOSITORY@sha256:DIGEST from this installation's registry"},
	{domain.ErrSetImageDenied, http.StatusForbidden, "set-image-denied", "check the set-image policies"},
	{domain.ErrIdentityProviderUnavailable, http.StatusServiceUnavailable, "identity-provider-unavailable", "retry later"},
	{domain.ErrAppNotFound, http.StatusNotFound, "app-not-found", "check the app name"},
	{domain.ErrAppServiceNotFound, http.StatusNotFound, "service-not-found", "check the service name"},
	{domain.ErrManifestNotFound, http.StatusNotFound, "manifest-not-found", "push the image before setting it"},
	{domain.ErrAppRevisionNotFound, http.StatusNotFound, "not-found", ""},
	{domain.ErrAppIntentNotFound, http.StatusNotFound, "not-found", ""},
	{domain.ErrAppOperationNotFound, http.StatusNotFound, "not-found", ""},
	{domain.ErrAppBusy, http.StatusConflict, "app-busy", "another operation of this app is running; retry later with a new key"},
	{domain.ErrAppStateConflict, http.StatusConflict, "state-conflict", ""},
}

// RetryAfter returns the Retry-After seconds for a retryable error, or "".
func RetryAfter(err error) string {
	switch {
	case errors.Is(err, domain.ErrAppBusy):
		return "5"
	case errors.Is(err, domain.ErrIdentityProviderUnavailable):
		return "30"
	default:
		return ""
	}
}

// WriteError answers err with its mapped status, Retry-After when the error
// is retryable, and the v3 error envelope.
func WriteError(w http.ResponseWriter, err error) {
	status, envelope := ErrorResponse(err)
	if after := RetryAfter(err); after != "" {
		w.Header().Set("Retry-After", after)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope)
}

// ErrorResponse maps an app or image change error to its HTTP status and v3
// error envelope. Unknown errors map to 500 without leaking the cause.
func ErrorResponse(err error) (int, dto.AppError) {
	for _, m := range errorMappings {
		if errors.Is(err, m.err) {
			return m.status, dto.AppError{Error: m.code, Message: err.Error(), Hint: m.hint}
		}
	}
	return http.StatusInternalServerError, dto.AppError{Error: "internal", Message: "app operation failed"}
}
