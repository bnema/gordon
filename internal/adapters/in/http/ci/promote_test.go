package ci

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
)

const promoteBody = `{"service":"web","image":"web@` + testDigest + `"}`

func promoteRequest(token, key, body string) *http.Request {
	req := withBearer(request(http.MethodPost, "/-/ci/apps/web/promote", []byte(body)), token)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	return req
}

func runningOp(key string) *domain.AppOperation {
	return &domain.AppOperation{Op: key, App: "web", InputRevision: "rev-2", Principal: webPrincipal.String()}
}

func TestPromote_CallsPortAsCIPrincipal(t *testing.T) {
	h := newHarness(t)
	h.apps.EXPECT().Promote(mock.Anything, in.PromoteInput{
		App: "web", Service: "web", Image: "web@" + testDigest, IdempotencyKey: "key-1",
		Principal: webPrincipal, LocalOwner: false,
	}).Return(runningOp("key-1"), nil).Once()
	h.apps.EXPECT().Show(mock.Anything, "web").Return(&in.AppDetail{Converged: true, Retained: in.AppRetainedView{Secrets: []string{"db-password"}}}, nil).Once()

	rec := h.serve(promoteRequest(h.ciToken(webPrincipal), "key-1", promoteBody))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var resp dto.AppDeployResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "key-1", resp.Op)
	assert.Equal(t, "web", resp.App)
	assert.Equal(t, dto.AppStatusRunning, resp.Status)
	require.NotNil(t, resp.Effective)
	assert.Nil(t, resp.Retained, "a CI principal never sees retained resource names")
	assert.NotContains(t, rec.Body.String(), "db-password")
}

func TestPromote_StatusMappingMatchesAdmin(t *testing.T) {
	cases := []struct {
		name   string
		op     *domain.AppOperation
		err    error
		status int
		code   string
	}{
		{"terminal success", &domain.AppOperation{Op: "k", App: "web", Outcome: domain.AppOutcomeSuccess}, nil, http.StatusOK, ""},
		{"denied", nil, fmt.Errorf("x: %w", domain.ErrPromotionDenied), http.StatusForbidden, "promotion-denied"},
		{"tag image", nil, domain.ErrInvalidPromotionImage, http.StatusBadRequest, "invalid-image"},
		{"missing manifest", nil, domain.ErrManifestNotFound, http.StatusNotFound, "manifest-not-found"},
		{"conflict", nil, domain.ErrAppStateConflict, http.StatusConflict, "state-conflict"},
		{"busy", nil, domain.ErrAppBusy, http.StatusConflict, "app-busy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.apps.EXPECT().Promote(mock.Anything, mock.Anything).Return(tc.op, tc.err).Once()
			h.apps.EXPECT().Show(mock.Anything, "web").Return(nil, domain.ErrAppNotFound).Maybe()
			rec := h.serve(promoteRequest(h.ciToken(webPrincipal), "k", promoteBody))
			assert.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.code != "" {
				var envelope dto.AppError
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
				assert.Equal(t, tc.code, envelope.Error)
			}
		})
	}
}

func TestPromote_RejectsBadRequestsWithoutCallingPort(t *testing.T) {
	h := newHarness(t)
	token := h.ciToken(webPrincipal)
	for name, req := range map[string]*http.Request{
		"missing key":   promoteRequest(token, "", promoteBody),
		"bad key":       promoteRequest(token, "has space", promoteBody),
		"unknown field": promoteRequest(token, "k", `{"service":"web","image":"x","local_owner":true}`),
		"no image":      promoteRequest(token, "k", `{"service":"web"}`),
	} {
		t.Run(name, func(t *testing.T) {
			rec := h.serve(req)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
	rec := h.serve(promoteRequest("", "k", promoteBody))
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "promote requires a ci-access token")
}

func TestOperationByKey_ScopedToPrincipal(t *testing.T) {
	h := newHarness(t)
	lookup := func(token, path string) *http.Request {
		return withBearer(request(http.MethodGet, path, nil), token)
	}

	op := runningOp("key-1")
	op.Steps = []domain.AppOperationStep{{ID: "service.web.replace", State: domain.AppStepPending, Diagnostics: []string{"app output"}}}
	h.apps.EXPECT().PrincipalOperationByKey(mock.Anything, "web", "key-1", webPrincipal).Return(op, nil).Once()
	rec := h.serve(lookup(h.ciToken(webPrincipal), "/-/ci/apps/web/operations/by-key/key-1"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp dto.AppDeployResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "key-1", resp.Op)
	assert.Equal(t, dto.AppStatusRunning, resp.Status)
	assert.NotContains(t, rec.Body.String(), "app output", "diagnostics are never returned to CI")

	// An operation recorded for another principal is reported missing by the port.
	h.apps.EXPECT().PrincipalOperationByKey(mock.Anything, "web", "theirs", webPrincipal).
		Return(nil, domain.ErrAppOperationNotFound).Once()
	rec = h.serve(lookup(h.ciToken(webPrincipal), "/-/ci/apps/web/operations/by-key/theirs"))
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// A principal without any policy for the app never reaches the port.
	rec = h.serve(lookup(h.ciToken(otherPrincipal), "/-/ci/apps/web/operations/by-key/key-1"))
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// Removing the policy hides lookups too (reload applies per request).
	empty := domain.PromotionPolicySet{}
	h.policies.Store(&empty)
	rec = h.serve(lookup(h.ciToken(webPrincipal), "/-/ci/apps/web/operations/by-key/key-1"))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
