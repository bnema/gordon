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

const promoteBody = `{"service":"web","image":"web@sha256:` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `"}`

func promoteRequest(t *testing.T, handler http.Handler, ctx context.Context, body string, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/apps/blog/promote", strings.NewReader(body)).WithContext(ctx)
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

func TestHandleAppPromote_StatusTable(t *testing.T) {
	write := domain.AdminScopeApps(domain.AdminActionWrite)
	running := &domain.AppOperation{Op: "k", Kind: domain.AppOperationKindPromote, App: "blog"}
	done := &domain.AppOperation{Op: "k", Kind: domain.AppOperationKindPromote, App: "blog", Outcome: domain.AppOutcomeSuccess}
	failed := &domain.AppOperation{Op: "k", Kind: domain.AppOperationKindPromote, App: "blog", Outcome: domain.AppOutcomeFailed}
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
		{name: "tag image", err: domain.ErrInvalidPromotionImage, status: http.StatusBadRequest, code: "invalid-image", calls: true},
		{name: "policy denied", err: domain.ErrPromotionDenied, status: http.StatusForbidden, code: "promotion-denied", calls: true},
		{name: "app missing", err: domain.ErrAppNotFound, status: http.StatusNotFound, code: "app-not-found", calls: true},
		{name: "service missing", err: domain.ErrAppServiceNotFound, status: http.StatusNotFound, code: "service-not-found", calls: true},
		{name: "manifest missing", err: domain.ErrManifestNotFound, status: http.StatusNotFound, code: "manifest-not-found", calls: true},
		{name: "cas lost", err: domain.ErrAppStateConflict, status: http.StatusConflict, code: "state-conflict", calls: true},
		{name: "identity provider down", err: domain.ErrIdentityProviderUnavailable, status: http.StatusServiceUnavailable, code: "identity-provider-unavailable", calls: true},
		{name: "unknown field", body: `{"service":"web","image":"x","extra":1}`, status: http.StatusBadRequest, code: "invalid-request"},
		{name: "missing image", body: `{"service":"web"}`, status: http.StatusBadRequest, code: "invalid-request"},
		{name: "trailing data", body: promoteBody + `{}`, status: http.StatusBadRequest, code: "invalid-request"},
		{name: "oversized", body: `{"service":"web","image":"` + strings.Repeat("a", 8<<10) + `"}`, status: http.StatusBadRequest, code: "invalid-request"},
		{name: "missing key", key: "-", status: http.StatusBadRequest},
		{name: "read scope only", scopes: []string{domain.AdminScopeApps(domain.AdminActionRead)}, status: http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			appSvc := inmocks.NewMockAppService(t)
			handler := appsTestHandler(t, appSvc)
			if tc.calls {
				appSvc.EXPECT().Promote(mock.Anything, mock.Anything).Return(tc.op, tc.err).Once()
				if tc.op != nil {
					appSvc.EXPECT().Show(mock.Anything, "blog").Return(nil, domain.ErrAppNotFound).Maybe()
				}
			}
			body, key, scopes := tc.body, tc.key, tc.scopes
			if body == "" {
				body = promoteBody
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
			rec := promoteRequest(t, handler, tokenCtx("ci-bot", scopes...), body, key)
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

func TestHandleAppPromote_TokenCallerIsGordonPrincipal(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)
	appSvc.EXPECT().Promote(mock.Anything, in.PromoteInput{
		App: "blog", Service: "web", Image: "web@sha256:" + strings.Repeat("a", 64), ExpectedRevision: "rev-1",
		IdempotencyKey: "k", Principal: domain.WorkloadPrincipal{Issuer: domain.GordonIssuer, Subject: "ci-bot"},
	}).Return(nil, domain.ErrPromotionDenied).Once()
	body := `{"service":"web","image":"web@sha256:` + strings.Repeat("a", 64) + `","expected_revision":"rev-1"}`
	rec := promoteRequest(t, handler, tokenCtx("ci-bot", domain.AdminScopeApps(domain.AdminActionWrite)), body, "k")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandleAppPromote_TokenNamedLocalOwnerIsNotLocal(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)
	appSvc.EXPECT().Promote(mock.Anything, mock.MatchedBy(func(input in.PromoteInput) bool {
		return !input.LocalOwner
	})).Return(nil, domain.ErrPromotionDenied).Once()
	rec := promoteRequest(t, handler, tokenCtx(LocalAuthoritySubject, domain.AdminScopeApps(domain.AdminActionWrite)), promoteBody, "k")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandleAppPromote_LocalSocketIsLocalOwner(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	local := appsTestHandler(t, appSvc).LocalAuthority()
	appSvc.EXPECT().Promote(mock.Anything, mock.MatchedBy(func(input in.PromoteInput) bool {
		return input.LocalOwner && input.Principal == domain.WorkloadPrincipal{Issuer: domain.GordonIssuer, Subject: LocalAuthoritySubject}
	})).Return(&domain.AppOperation{Op: "k", App: "blog"}, nil).Once()
	appSvc.EXPECT().Show(mock.Anything, "blog").Return(nil, domain.ErrAppNotFound).Maybe()
	rec := promoteRequest(t, local, context.Background(), promoteBody, "k")
	assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.True(t, localPathAllowed(http.MethodPost, "/apps/blog/promote"))
	assert.False(t, localPathAllowed(http.MethodGet, "/apps/blog/promote"))
}
