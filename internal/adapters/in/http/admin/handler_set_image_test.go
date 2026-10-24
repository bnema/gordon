package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	in "github.com/bnema/gordon/internal/boundaries/in"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

const setImageBody = `{"service":"web","image":"web@sha256:` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `"}`

func setImageRequest(t *testing.T, handler http.Handler, ctx context.Context, body string, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/apps/blog/set-image", strings.NewReader(body)).WithContext(ctx)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func tokenCtx(subject string, scopes ...string) context.Context {
	ctx := ctxWithScopes(scopes...)
	ctx = context.WithValue(ctx, domain.ContextKeySubject, subject)
	return context.WithValue(ctx, domain.TokenClaimsKey, &domain.TokenClaims{Subject: subject, Scopes: scopes})
}

func TestHandleAppSetImage_StatusTable(t *testing.T) {
	write := domain.AdminScopeApps(domain.AdminActionWrite)
	running := &domain.AppOperation{Op: "k", Kind: domain.AppOperationKindSetImage, App: "blog"}
	done := &domain.AppOperation{Op: "k", Kind: domain.AppOperationKindSetImage, App: "blog", Outcome: domain.AppOutcomeSuccess}
	failed := &domain.AppOperation{Op: "k", Kind: domain.AppOperationKindSetImage, App: "blog", Outcome: domain.AppOutcomeFailed}
	tests := []struct {
		name   string
		body   string
		key    string
		scopes []string
		op     *domain.AppOperation
		err    error
		status int
		code   string
		calls  bool
	}{
		{name: "accepted", op: running, status: http.StatusAccepted, calls: true},
		{name: "terminal replay", op: done, status: http.StatusOK, calls: true},
		{name: "replayed failure", op: failed, err: domain.ErrAppStateConflict, status: http.StatusConflict, calls: true},
		{name: "tag image", err: domain.ErrInvalidImageReference, status: http.StatusBadRequest, code: "invalid-image", calls: true},
		{name: "policy denied", err: domain.ErrSetImageDenied, status: http.StatusForbidden, code: "set-image-denied", calls: true},
		{name: "app missing", err: domain.ErrAppNotFound, status: http.StatusNotFound, code: "app-not-found", calls: true},
		{name: "service missing", err: domain.ErrAppServiceNotFound, status: http.StatusNotFound, code: "service-not-found", calls: true},
		{name: "manifest missing", err: domain.ErrManifestNotFound, status: http.StatusNotFound, code: "manifest-not-found", calls: true},
		{name: "cas lost", err: domain.ErrAppStateConflict, status: http.StatusConflict, code: "state-conflict", calls: true},
		{name: "identity provider down", err: domain.ErrIdentityProviderUnavailable, status: http.StatusServiceUnavailable, code: "identity-provider-unavailable", calls: true},
		{name: "not wired", err: domain.ErrSetImageUnavailable, status: http.StatusInternalServerError, code: "internal", calls: true},
		{name: "trailing brace", body: setImageBody + `}`, status: http.StatusBadRequest, code: "invalid-request"},
		{name: "non-canonical field", body: `{"Service":"web","image":"x"}`, status: http.StatusBadRequest, code: "invalid-request"},
		{name: "unknown field", body: `{"service":"web","image":"x","extra":1}`, status: http.StatusBadRequest, code: "invalid-request"},
		{name: "missing image", body: `{"service":"web"}`, status: http.StatusBadRequest, code: "invalid-request"},
		{name: "trailing data", body: setImageBody + `{}`, status: http.StatusBadRequest, code: "invalid-request"},
		{name: "oversized", body: `{"service":"web","image":"` + strings.Repeat("a", 8<<10) + `"}`, status: http.StatusBadRequest, code: "invalid-request"},
		{name: "missing key", key: "-", status: http.StatusBadRequest},
		{name: "read scope only", scopes: []string{domain.AdminScopeApps(domain.AdminActionRead)}, status: http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			appSvc := inmocks.NewMockAppService(t)
			handler := appsTestHandler(t, appSvc)
			if tc.calls {
				appSvc.EXPECT().SetImage(mock.Anything, mock.Anything).Return(tc.op, tc.err).Once()
				if tc.op != nil {
					appSvc.EXPECT().Show(mock.Anything, "blog").Return(nil, domain.ErrAppNotFound).Maybe()
				}
			}
			body, key, scopes := tc.body, tc.key, tc.scopes
			if body == "" {
				body = setImageBody
			}
			switch key {
			case "":
				key = "k"
			case "-":
				key = ""
			}
			if scopes == nil {
				scopes = []string{write}
			}
			rec := setImageRequest(t, handler, tokenCtx("ci-bot", scopes...), body, key)
			assert.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.code != "" {
				var envelope dto.AppError
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
				assert.Equal(t, tc.code, envelope.Error)
			}
			if tc.op != nil {
				var resp dto.AppDeployResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
				assert.Equal(t, "k", resp.Op)
			}
		})
	}
}

func TestHandleAppSetImage_TokenCallerIsGordonPrincipal(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)
	appSvc.EXPECT().SetImage(mock.Anything, in.SetImageInput{
		App: "blog", Service: "web", Image: "web@sha256:" + strings.Repeat("a", 64), ExpectedRevision: "rev-1",
		IdempotencyKey: "k", Principal: domain.WorkloadPrincipal{Issuer: domain.GordonIssuer, Subject: "ci-bot"},
	}).Return(nil, domain.ErrSetImageDenied).Once()
	body := `{"service":"web","image":"web@sha256:` + strings.Repeat("a", 64) + `","expected_revision":"rev-1"}`
	rec := setImageRequest(t, handler, tokenCtx("ci-bot", domain.AdminScopeApps(domain.AdminActionWrite)), body, "k")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandleAppSetImage_TokenNamedLocalOwnerIsNotLocal(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)
	appSvc.EXPECT().SetImage(mock.Anything, mock.MatchedBy(func(input in.SetImageInput) bool {
		return !input.LocalOwner
	})).Return(nil, domain.ErrSetImageDenied).Once()
	rec := setImageRequest(t, handler, tokenCtx(LocalAuthoritySubject, domain.AdminScopeApps(domain.AdminActionWrite)), setImageBody, "k")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestHandleAppSetImage_LocalOwnerNeedsTheSocketMarker proves local-owner
// rights come only from LocalAuthority: a context carrying the local-owner
// subject without a token (and without the marker) is not the local owner.
func TestHandleAppSetImage_LocalOwnerNeedsTheSocketMarker(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)
	appSvc.EXPECT().SetImage(mock.Anything, mock.MatchedBy(func(input in.SetImageInput) bool {
		return !input.LocalOwner
	})).Return(nil, domain.ErrSetImageDenied).Once()
	ctx := context.WithValue(ctxWithScopes(domain.AdminScopeApps(domain.AdminActionWrite)), domain.ContextKeySubject, LocalAuthoritySubject)
	rec := setImageRequest(t, handler, ctx, setImageBody, "k")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandleAppSetImage_BusyAppSetsRetryAfter(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)
	appSvc.EXPECT().SetImage(mock.Anything, mock.Anything).Return(nil, domain.ErrAppBusy).Once()
	rec := setImageRequest(t, handler, tokenCtx("ci-bot", domain.AdminScopeApps(domain.AdminActionWrite)), setImageBody, "k")
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "5", rec.Header().Get("Retry-After"))
	var envelope dto.AppError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, "app-busy", envelope.Error)
}

func TestHandleAppSetImage_LocalSocketIsLocalOwner(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	local := appsTestHandler(t, appSvc).LocalAuthority()
	appSvc.EXPECT().SetImage(mock.Anything, mock.MatchedBy(func(input in.SetImageInput) bool {
		return input.LocalOwner && input.Principal == domain.WorkloadPrincipal{Issuer: domain.GordonIssuer, Subject: LocalAuthoritySubject}
	})).Return(&domain.AppOperation{Op: "k", App: "blog"}, nil).Once()
	appSvc.EXPECT().Show(mock.Anything, "blog").Return(nil, domain.ErrAppNotFound).Maybe()
	rec := setImageRequest(t, local, context.Background(), setImageBody, "k")
	assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.True(t, localPathAllowed(http.MethodPost, "/apps/blog/set-image"))
	assert.False(t, localPathAllowed(http.MethodGet, "/apps/blog/set-image"))
}

func TestSetImage_KeyRoundTrip(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)
	key := "AZaz09-_.~"
	op := &domain.AppOperation{Op: key, App: "blog"}
	appSvc.EXPECT().SetImage(mock.Anything, mock.MatchedBy(func(input in.SetImageInput) bool { return input.IdempotencyKey == key })).Return(op, nil).Once()
	appSvc.EXPECT().Show(mock.Anything, "blog").Return(nil, domain.ErrAppNotFound).Maybe()
	appSvc.EXPECT().OperationByKey(mock.Anything, "blog", key).Return(op, nil).Once()
	require.Equal(t, http.StatusAccepted, setImageRequest(t, handler, tokenCtx("ci-bot", "admin:apps:write"), setImageBody, key).Code)
	rec := appsRequest(t, handler, http.MethodGet, "/admin/apps/blog/operations/by-key/"+key, nil, "admin:apps:read")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, http.StatusBadRequest, setImageRequest(t, handler, tokenCtx("ci-bot", "admin:apps:write"), setImageBody, "a/b").Code)
	// The strict mock permits exactly one SetImage call: rejection cannot create an operation.
}
