package ci

import (
	"context"
	"net/http"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/domain"
)

// caller is the identity a valid ci-access token carries.
type caller struct {
	principal domain.WorkloadPrincipal
	scopes    []string
}

type callerKey struct{}

// authenticate accepts only a ci-access Bearer token. Main-host access
// tokens, stored tokens and raw OIDC JWTs are all refused.
func (h *Handler) authenticate(r *http.Request) (caller, bool) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || raw == "" {
		return caller{}, false
	}
	principal, scopes, err := h.deps.Auth.ValidateCIAccessToken(r.Context(), raw)
	if err != nil {
		log := zerowrap.FromCtx(r.Context())
		log.Debug().Msg("ci-access token rejected")
		return caller{}, false
	}
	return caller{principal: principal, scopes: scopes}, true
}

// requireCaller is the CIAuth step: it stores the caller in the request
// context or answers 401 with the CI host challenge.
func (h *Handler) requireCaller(w http.ResponseWriter, r *http.Request, registry bool) (*http.Request, caller, bool) {
	c, ok := h.authenticate(r)
	if !ok {
		h.challenge(w, registry, "")
		return r, caller{}, false
	}
	log := zerowrap.FromCtx(r.Context())
	log.Debug().Str("principal", c.principal.String()).Msg("ci-access token accepted")
	return r.WithContext(context.WithValue(r.Context(), callerKey{}, c)), c, true
}

// challenge answers 401 with a Bearer challenge pointing at the CI token
// endpoint. scope, when set, names the scope the client must request.
func (h *Handler) challenge(w http.ResponseWriter, registry bool, scope string) {
	value := `Bearer realm="` + h.realm() + `",service="` + h.ciDomain() + `"`
	if scope != "" {
		value += `,scope="` + scope + `"`
	}
	w.Header().Set("WWW-Authenticate", value)
	if registry {
		writeRegistryError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	writeJSON(w, http.StatusUnauthorized, dto.AppError{Error: "unauthorized", Message: "a ci-access token is required"})
}
