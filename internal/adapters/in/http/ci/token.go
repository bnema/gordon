package ci

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/http/appapi"
	authhttp "github.com/bnema/gordon/internal/adapters/in/http/auth"
	"github.com/bnema/gordon/internal/domain"
)

// handleToken serves GET /auth/token: it exchanges a workload identity JWT
// (the Basic password; the username is ignored) for a ci-access token whose
// scopes are the requested repository scopes the set-image policies grant
// the identity. The response has the shape of the main token endpoint.
func (h *Handler) handleToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	log := zerowrap.FromCtx(r.Context())

	username, jwt, ok := r.BasicAuth()
	if !ok || jwt == "" || len(jwt) > maxJWTBytes {
		log.Debug().Bool("basic", ok).Int("length", len(jwt)).Msg("CI token request without a usable identity token")
		unauthorizedBasic(w)
		return
	}
	log = log.WithField("username", redactUsername(username))

	principal, err := h.verify(r.Context(), jwt)
	if err != nil {
		if errors.Is(err, domain.ErrIdentityProviderUnavailable) {
			log.Warn().Err(err).Msg("CI token request: identity provider unavailable")
			appapi.WriteError(w, err)
			return
		}
		log.Debug().Msg("CI token request: identity token rejected")
		unauthorizedBasic(w)
		return
	}

	scopes, ok := h.grantedScopes(r, principal)
	if !ok {
		log.Warn().Str("principal", principal.String()).Msg("CI token request: no set-image policy grants the requested scope")
		writeJSON(w, http.StatusForbidden, dto.ErrorResponse{Error: "forbidden: no set-image policy grants the requested scope"})
		return
	}
	h.issue(w, r, principal, scopes)
}

// verify runs workload identity verification under a waiter slot and a
// bounded deadline, so requests stuck behind a JWKS refresh cannot pile up.
func (h *Handler) verify(ctx context.Context, jwt string) (domain.WorkloadPrincipal, error) {
	select {
	case h.waiters <- struct{}{}:
		defer func() { <-h.waiters }()
	default:
		return domain.WorkloadPrincipal{}, errors.Join(domain.ErrIdentityProviderUnavailable, errors.New("too many pending identity verifications"))
	}
	ctx, cancel := context.WithTimeout(ctx, maxVerifyWait)
	defer cancel()
	return h.deps.Identity.Verify(ctx, jwt)
}

// grantedScopes intersects the requested repository scopes with push and
// pull on each repository a policy grants principal. A request without a
// scope (a registry login) gets an unscoped token; a principal without any
// policy, or a request no policy covers, gets nothing.
func (h *Handler) grantedScopes(r *http.Request, principal domain.WorkloadPrincipal) ([]string, bool) {
	repos := h.deps.Policies().Repositories(principal)
	if len(repos) == 0 {
		return nil, false
	}
	requested := requestedRepositoryScopes(r)
	if len(requested) == 0 {
		return nil, true
	}
	granted := make([]string, 0, len(repos))
	for _, repo := range repos {
		granted = append(granted, "repository:"+repo+":"+domain.ScopeActionPush+","+domain.ScopeActionPull)
	}
	effective := authhttp.IntersectScopes(requested, granted)
	return effective, len(effective) > 0
}

// requestedRepositoryScopes returns the well-formed repository scopes of
// the query. Other scope types are never granted on the CI host.
func requestedRepositoryScopes(r *http.Request) []string {
	var scopes []string
	for _, raw := range r.URL.Query()["scope"] {
		scope, err := domain.ParseScope(raw)
		if err == nil && scope.IsRepositoryScope() {
			scopes = append(scopes, raw)
		}
	}
	return scopes
}

func (h *Handler) issue(w http.ResponseWriter, r *http.Request, principal domain.WorkloadPrincipal, scopes []string) {
	log := zerowrap.FromCtx(r.Context())
	ttl := min(h.deps.Auth.GetAccessTokenTTL(), maxCIAccessTTL)
	token, err := h.deps.Auth.GenerateCIAccessToken(r.Context(), principal, scopes, ttl)
	if err != nil {
		log.Error().Err(err).Msg("failed to mint ci-access token")
		writeJSON(w, http.StatusInternalServerError, dto.ErrorResponse{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, dto.TokenResponse{
		Token:     token,
		ExpiresIn: int(ttl.Seconds()),
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
	})
	log.Info().Str("principal", principal.String()).Strs("scopes", scopes).Msg("ci-access token issued")
}

func unauthorizedBasic(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="Gordon CI"`)
	writeJSON(w, http.StatusUnauthorized, dto.ErrorResponse{Error: "unauthorized"})
}
