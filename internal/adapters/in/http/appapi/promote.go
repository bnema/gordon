// Package appapi holds the HTTP mapping of app mutations that more than one
// driving adapter serves: the admin API and the CI host both decode
// promotion requests, call the same in.AppService port, and map its results
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

// MaxPromoteRequestSize caps a promotion request body.
const MaxPromoteRequestSize = 4 << 10

// ErrInvalidRequest marks a malformed promotion request body.
var ErrInvalidRequest = errors.New("invalid promotion request")

// DecodePromoteRequest strictly decodes one size-capped promotion body:
// unknown fields, trailing data, and missing service or image are refused.
func DecodePromoteRequest(w http.ResponseWriter, r *http.Request) (dto.AppPromoteRequest, error) {
	var req dto.AppPromoteRequest
	if r.Body == nil {
		return req, fmt.Errorf("%w: body required", ErrInvalidRequest)
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxPromoteRequestSize))
	if err != nil {
		return req, fmt.Errorf("%w: body unreadable or too large", ErrInvalidRequest)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return req, fmt.Errorf("%w: invalid JSON", ErrInvalidRequest)
	}
	if decoder.More() {
		return req, fmt.Errorf("%w: trailing data", ErrInvalidRequest)
	}
	if strings.TrimSpace(req.Service) == "" || strings.TrimSpace(req.Image) == "" {
		return req, fmt.Errorf("%w: service and image are required", ErrInvalidRequest)
	}
	return req, nil
}

// PromoteInput builds the port input from a decoded request and the caller
// identity the driving adapter established.
func PromoteInput(app, key string, req dto.AppPromoteRequest, principal domain.WorkloadPrincipal, localOwner bool) in.PromoteInput {
	return in.PromoteInput{
		App: app, Service: req.Service, Image: req.Image, ExpectedRevision: req.ExpectedRevision,
		IdempotencyKey: key, Principal: principal, LocalOwner: localOwner,
	}
}

// PromoteOutcome classifies a Promote result. Respond is false when no
// journal exists: the request was refused before any work and is answered
// with ErrorResponse. Otherwise the journal is the body and Status is 409
// for a replay that failed or is still unfinished, 202 for a newly owned
// running operation, and 200 for a terminal success.
func PromoteOutcome(op *domain.AppOperation, err error) (status int, respond bool) {
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
	{domain.ErrInvalidPromotionImage, http.StatusBadRequest, "invalid-image", "use REPOSITORY@sha256:DIGEST from this installation's registry"},
	{domain.ErrPromotionDenied, http.StatusForbidden, "promotion-denied", "check the promotion policies"},
	{domain.ErrIdentityProviderUnavailable, http.StatusServiceUnavailable, "identity-provider-unavailable", "retry later"},
	{domain.ErrAppNotFound, http.StatusNotFound, "app-not-found", "check the app name"},
	{domain.ErrAppServiceNotFound, http.StatusNotFound, "service-not-found", "check the service name"},
	{domain.ErrManifestNotFound, http.StatusNotFound, "manifest-not-found", "push the image before promoting it"},
	{domain.ErrAppRevisionNotFound, http.StatusNotFound, "not-found", ""},
	{domain.ErrAppIntentNotFound, http.StatusNotFound, "not-found", ""},
	{domain.ErrAppOperationNotFound, http.StatusNotFound, "not-found", ""},
	{domain.ErrAppStateConflict, http.StatusConflict, "state-conflict", ""},
}

// ErrorResponse maps an app or promotion error to its HTTP status and v3
// error envelope. Unknown errors map to 500 without leaking the cause.
func ErrorResponse(err error) (int, dto.AppError) {
	for _, m := range errorMappings {
		if errors.Is(err, m.err) {
			return m.status, dto.AppError{Error: m.code, Message: err.Error(), Hint: m.hint}
		}
	}
	return http.StatusInternalServerError, dto.AppError{Error: "internal", Message: "app operation failed"}
}
