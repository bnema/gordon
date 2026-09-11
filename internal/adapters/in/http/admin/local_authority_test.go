package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	in "github.com/bnema/gordon/internal/boundaries/in"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func localRequest(t *testing.T, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestLocalAuthorityInjectsLeastPrivilegeScopes proves the socket identity
// carries explicit scopes: apps read/write and logs read, and nothing else.
func TestLocalAuthorityInjectsLeastPrivilegeScopes(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	appSvc.EXPECT().List(mock.MatchedBy(func(ctx context.Context) bool {
		if GetSubject(ctx) != LocalAuthoritySubject {
			t.Errorf("subject = %q, want %q", GetSubject(ctx), LocalAuthoritySubject)
		}
		return HasAccess(ctx, domain.AdminResourceApps, domain.AdminActionRead) &&
			HasAccess(ctx, domain.AdminResourceApps, domain.AdminActionWrite) &&
			HasAccess(ctx, domain.AdminResourceLogs, domain.AdminActionRead) &&
			!HasAccess(ctx, domain.AdminResourceConfig, domain.AdminActionRead) &&
			!HasAccess(ctx, domain.AdminResourceVolumes, domain.AdminActionWrite) &&
			!HasAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionRead)
	})).Return([]in.AppSummary(nil), nil).Once()

	rec := localRequest(t, handler.LocalAuthority(), http.MethodGet, "/admin/apps")
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestLocalAuthorityAllowsAppEndpoints(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	appSvc.EXPECT().Show(mock.Anything, "blog").Return(&in.AppDetail{}, nil).Once()

	rec := localRequest(t, handler.LocalAuthority(), http.MethodGet, "/admin/apps/blog")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestLocalAuthorityDeniesUnrelatedRoutes(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)
	local := handler.LocalAuthority()

	for _, target := range []string{
		"/admin/config",
		"/admin/auth/verify",
		"/admin/status",
		"/admin/health",
		"/admin/reload",
		"/admin/backups/blog",
		"/admin/secrets/blog",
		"/admin/volumes",
		"/admin/volumes/prune",
		"/admin/tags/repo",
		"/admin/images",
		"/admin/networks",
		"/admin/tls/status",
		"/admin/traffic/status",
		"/someone-elses-path",
	} {
		rec := localRequest(t, local, http.MethodGet, target)
		assert.Equal(t, http.StatusForbidden, rec.Code, "target %s", target)

		var envelope dto.ErrorResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
		assert.Equal(t, "local administration is limited to app endpoints", envelope.Error)
	}
}

func TestLocalAuthorityDeniesProcessLogsAndAllowsAppContainerLogs(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)
	local := handler.LocalAuthority()

	rec := localRequest(t, local, http.MethodGet, "/admin/logs")
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// The app-specific route reaches the log handler; it answers 503 only
	// because this focused fixture has no log service wired.
	rec = localRequest(t, local, http.MethodGet, "/admin/logs/blog")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// TestLocalAuthorityClassifiesByPathOnly ensures classification uses the URL
// path alone: a query string never widens access and a lookalike prefix never
// matches.
func TestLocalAuthorityClassifiesByPathOnly(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)
	local := handler.LocalAuthority()

	rec := localRequest(t, local, http.MethodGet, "/admin/config?x=apps")
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = localRequest(t, local, http.MethodGet, "/admin/appsX")
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = localRequest(t, local, http.MethodGet, "/apps")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestLocalAuthorityRejectsNonCanonicalTraversal pins that a path which
// canonicalizes outside the local allowlist is denied rather than dispatched
// to a different admin route.
func TestLocalAuthorityRejectsNonCanonicalTraversal(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)
	local := handler.LocalAuthority()

	for _, target := range []string{
		"/admin/apps/../config",
		"/admin/apps/..%2fconfig",
		"/admin/logs/../../config",
		"/admin/apps/../../status",
	} {
		rec := localRequest(t, local, http.MethodGet, target)
		assert.Equal(t, http.StatusForbidden, rec.Code, "target %s", target)
	}
}

// TestLocalAuthorityCanonicalizesTrailingSlash ensures a trailing slash lists
// apps rather than being denied.
func TestLocalAuthorityCanonicalizesTrailingSlash(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	appSvc.EXPECT().List(mock.Anything).Return([]in.AppSummary(nil), nil).Once()

	rec := localRequest(t, handler.LocalAuthority(), http.MethodGet, "/admin/apps/")
	assert.Equal(t, http.StatusOK, rec.Code)
}
