package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/http/admin"
	authhttp "github.com/bnema/gordon/internal/adapters/in/http/auth"
	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/auth"
)

func TestWorkloadIdentityReceivesIssuersAtStartupAndReload(t *testing.T) {
	v, cfg := readPromotionConfig(t, promotionTOML)
	identity := inmocks.NewMockWorkloadIdentityService(t)
	svc := &services{workloadIdentitySvc: identity}

	startup, err := buildPromotionSnapshot(cfg)
	require.NoError(t, err)
	svc.promotionSnapshot.Store(&startup)
	identity.EXPECT().SetIssuers(startup.Issuers()).Once()
	svc.reloadWorkloadIdentity()

	cfg.TrustedIssuers[0].Audience = "new-aud"
	identity.EXPECT().SetIssuers(mock.MatchedBy(func(issuers []domain.TrustedIssuer) bool {
		return len(issuers) == 1 && issuers[0].Audience == "new-aud"
	})).Once()
	r := &reloadRuntime{v: v, svc: svc}
	require.NoError(t, r.Apply(context.Background(), cfg))

	// An invalid reload keeps the previous snapshot and never reaches SetIssuers
	// (the strict mock fails on an unexpected call).
	cfg.PromotionPolicies[0].Subjects = nil
	require.Error(t, r.Apply(context.Background(), cfg))
}

func TestInitWorkloadIdentityDoesNotBlockOnUnreachableIssuer(t *testing.T) {
	_, cfg := readPromotionConfig(t, promotionTOML)
	snapshot, err := buildPromotionSnapshot(cfg)
	require.NoError(t, err)
	svc := &services{}
	svc.promotionSnapshot.Store(&snapshot)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	si := &serviceInit{ctx: ctx, cfg: cfg, svc: svc}

	done := make(chan struct{})
	go func() {
		si.initWorkloadIdentity()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("startup blocked on issuer discovery")
	}
	require.NotNil(t, svc.workloadIdentitySvc)
}

// TestCIAccessTokenRejectedOnMainHost drives a real ci-access token through the
// registry, admin and token-exchange authentication of the main host.
func TestCIAccessTokenRejectedOnMainHost(t *testing.T) {
	log := zerowrap.Default()
	authSvc := auth.NewService(auth.Config{Enabled: true, AuthType: domain.AuthTypeToken, TokenSecret: []byte("0123456789abcdef0123456789abcdef")},
		outmocks.NewMockTokenStore(t), log)
	principal := domain.WorkloadPrincipal{Issuer: "https://issuer.example", Subject: "repo:acme/web"}
	ciToken, err := authSvc.GenerateCIAccessToken(context.Background(), principal, []string{"repository:web:push,pull"}, 0)
	require.NoError(t, err)
	accessToken, err := authSvc.GenerateAccessToken(context.Background(), "deployer", []string{"repository:web:push,pull", "admin:*:*"}, 5*time.Minute)
	require.NoError(t, err)

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	registry := middleware.RegistryAuthV2(authSvc, middleware.InternalRegistryAuth{}, nil, log)(ok)
	adminAPI := admin.AuthMiddleware(authSvc, nil, nil, nil, log)(ok)
	tokenExchange := authhttp.NewHandler(authSvc, authhttp.InternalAuth{}, log)

	serve := func(h http.Handler, method, path string, set func(*http.Request)) int {
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = "203.0.113.10:4242"
		req.Header.Set("X-Forwarded-Proto", "https")
		set(req)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	bearer := func(token string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
	}
	basic := func(user, token string) func(*http.Request) {
		return func(r *http.Request) { r.SetBasicAuth(user, token) }
	}

	// Control: the same paths accept an ordinary ephemeral access token.
	require.Equal(t, http.StatusOK, serve(registry, http.MethodGet, "/v2/", bearer(accessToken)))
	require.Equal(t, http.StatusOK, serve(adminAPI, http.MethodGet, "/admin/status", bearer(accessToken)))

	require.Equal(t, http.StatusUnauthorized, serve(registry, http.MethodGet, "/v2/", bearer(ciToken)))
	require.Equal(t, http.StatusUnauthorized, serve(registry, http.MethodPut, "/v2/web/manifests/latest", bearer(ciToken)))
	require.Equal(t, http.StatusUnauthorized, serve(registry, http.MethodGet, "/v2/", basic(principal.String(), ciToken)))
	require.Equal(t, http.StatusUnauthorized, serve(adminAPI, http.MethodGet, "/admin/status", bearer(ciToken)))
	require.Equal(t, http.StatusUnauthorized, serve(tokenExchange, http.MethodGet, "/auth/token?scope=repository:web:push", basic(principal.String(), ciToken)))
}
