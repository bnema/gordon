package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

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

func TestLocalAuthorityInjectsLeastPrivilegeScopes(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	appSvc.EXPECT().List(mock.MatchedBy(func(ctx context.Context) bool {
		assert.Equal(t, LocalAuthoritySubject, GetSubject(ctx))
		for _, access := range []struct{ resource, action string }{
			{domain.AdminResourceApps, domain.AdminActionRead},
			{domain.AdminResourceApps, domain.AdminActionWrite},
			{domain.AdminResourceStatus, domain.AdminActionRead},
			{domain.AdminResourceConfig, domain.AdminActionRead},
			{domain.AdminResourceConfig, domain.AdminActionWrite},
			{domain.AdminResourceSecrets, domain.AdminActionRead},
			{domain.AdminResourceSecrets, domain.AdminActionWrite},
			{domain.AdminResourceLogs, domain.AdminActionRead},
			{domain.AdminResourceVolumes, domain.AdminActionRead},
			{domain.AdminResourceVolumes, domain.AdminActionWrite},
		} {
			assert.True(t, HasAccess(ctx, access.resource, access.action), "%s:%s", access.resource, access.action)
		}
		assert.False(t, HasAccess(ctx, domain.AdminResourceRoutes, domain.AdminActionRead))
		assert.False(t, HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionWrite))
		assert.False(t, HasAccess(ctx, domain.AdminResourceLogs, domain.AdminActionWrite))
		assert.False(t, HasAccess(ctx, domain.AdminResourceAll, domain.AdminActionAll))
		return true
	})).Return([]in.AppSummary(nil), nil).Once()

	rec := localRequest(t, handler.LocalAuthority(), http.MethodGet, "/admin/apps")
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestLocalAuthorityCanonicalAllowlist(t *testing.T) {
	t.Parallel()
	allowed := []struct{ method, path string }{
		{http.MethodGet, "/status"},
		{http.MethodGet, "/tls/status"},
		{http.MethodGet, "/traffic/status"},
		{http.MethodGet, "/config"},
		{http.MethodGet, "/networks"},
		{http.MethodGet, "/volumes"},
		{http.MethodPost, "/volumes/prune"},
		{http.MethodGet, "/images"},
		{http.MethodPost, "/images/prune"},
		{http.MethodPost, "/reload"},
		{http.MethodGet, "/tags/repository"},
		{http.MethodGet, "/logs"},
		{http.MethodGet, "/logs/blog/web"},
		{http.MethodGet, "/secrets/blog.example.com"},
		{http.MethodPost, "/secrets/blog.example.com"},
		{http.MethodDelete, "/secrets/blog.example.com/API_KEY"},
		{http.MethodGet, "/backups"},
		{http.MethodGet, "/backups/status"},
		{http.MethodGet, "/backups/shop"},
		{http.MethodPost, "/backups/shop"},
		{http.MethodGet, "/backups/volumes"},
		{http.MethodPost, "/backups/volumes"},
		{http.MethodGet, "/backups/volumes/status"},
		{http.MethodGet, "/backups/volumes/blog.example.com"},
		{http.MethodPost, "/backups/volumes/blog.example.com"},
		{http.MethodGet, "/apps"},
		{http.MethodPost, "/apps/apply"},
		{http.MethodGet, "/apps/blog"},
		{http.MethodGet, "/apps/blog/diff"},
		{http.MethodPost, "/apps/blog/deploy"},
		{http.MethodPost, "/apps/blog/restart"},
		{http.MethodPost, "/apps/blog/stop"},
		{http.MethodPost, "/apps/blog/start"},
		{http.MethodPost, "/apps/blog/remove"},
		{http.MethodPost, "/apps/blog/secrets/set"},
		{http.MethodPost, "/apps/blog/secrets/delete"},
		{http.MethodGet, "/apps/blog/operations/by-key/request-1"},
	}
	for _, tc := range allowed {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			assert.True(t, localPathAllowed(tc.method, tc.path))
		})
	}
}

func TestLocalAuthorityDeniesNearMissesAndNonCanonicalPaths(t *testing.T) {
	t.Parallel()
	denied := []struct{ method, path string }{
		{http.MethodPost, "/status"},
		{http.MethodGet, "/reload"},
		{http.MethodPost, "/config"},
		{http.MethodGet, "/volumes/prune"},
		{http.MethodGet, "/images/prune"},
		{http.MethodGet, "/auth/verify"},
		{http.MethodGet, "/auth/tokens"},
		{http.MethodPost, "/auth/tokens"},
		{http.MethodGet, "/health"},
		{http.MethodGet, "/appsX"},
		{http.MethodGet, "/secrets"},
		{http.MethodDelete, "/secrets/blog"},
		{http.MethodGet, "/images/anything"},
		{http.MethodGet, "/tags/repository/extra"},
		{http.MethodGet, "/logs/blog"},
		{http.MethodGet, "/backups/volumes/status/extra"},
		{http.MethodGet, "/apps/blog/operations/by-key/key/extra"},
		{http.MethodGet, "/"},
	}
	for _, tc := range denied {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			assert.False(t, localPathAllowed(tc.method, tc.path))
		})
	}
}

func TestLocalAuthorityRejectsNonAdminAndTraversalRequests(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	local := appsTestHandler(t, appSvc).LocalAuthority()
	for _, target := range []string{
		"/apps",
		"/admin/auth/tokens",
		"/admin/apps/",
		"/admin/apps/../config",
		"/admin/apps/..%2fconfig",
		"/admin/logs/../../config",
	} {
		rec := localRequest(t, local, http.MethodGet, target)
		assert.Equal(t, http.StatusForbidden, rec.Code, target)
	}
}
