package ci

import (
	"net/http"
	"net/url"
	"slices"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/in/http/registry/route"
	"github.com/bnema/gordon/internal/domain"
)

// serveRegistry gates the push subset of the registry API: allowlist,
// ci-access authentication, the current promotion policy, then the token
// scope. Only then does the request reach the raw registry handler.
func (h *Handler) serveRegistry(w http.ResponseWriter, r *http.Request) {
	op, ok := allowedRegistryRoute(r)
	if !ok {
		writeRegistryError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		return
	}
	r, c, ok := h.requireCaller(w, r, true)
	if !ok {
		return
	}
	if op.Kind != route.KindBase && !h.mayAccess(w, r, c, op) {
		return
	}
	h.deps.Registry.ServeHTTP(w, r)
}

// mayAccess checks the repository against the current policies (so a
// reload applies to tokens already issued) and the action against the
// token scopes. A missing scope is challenged so registry clients fetch a
// token for it.
func (h *Handler) mayAccess(w http.ResponseWriter, r *http.Request, c caller, op route.Operation) bool {
	if !slices.Contains(h.deps.Policies().Repositories(c.principal), op.Repository) {
		log := zerowrap.FromCtx(r.Context())
		log.Warn().
			Str("principal", c.principal.String()).
			Str("repository", op.Repository).
			Msg("CI push denied: repository outside the promotion policies")
		writeRegistryError(w, http.StatusForbidden, "DENIED", "repository is not allowed for this identity")
		return false
	}
	action := domain.ScopeActionPush
	if r.Method == http.MethodHead {
		action = domain.ScopeActionPull
	}
	if !domain.ScopesGrantRegistryAccess(c.scopes, op.Repository, action) {
		scope := "repository:" + op.Repository + ":" + action
		if action == domain.ScopeActionPush {
			scope += "," + domain.ScopeActionPull
		}
		challenge(w, r, true, scope)
		return false
	}
	return true
}

// allowedRegistryRoute returns the parsed operation when the method and
// path are in the CI push allowlist. The repository is parsed with the
// same parser the registry handler dispatches on.
func allowedRegistryRoute(r *http.Request) (route.Operation, bool) {
	op, err := route.Parse(r.URL.Path)
	if err != nil {
		return op, false
	}
	switch op.Kind {
	case route.KindBase:
		return op, r.Method == http.MethodGet && r.URL.Path == "/v2/"
	case route.KindBlob:
		return op, r.Method == http.MethodHead
	case route.KindManifest:
		return op, r.Method == http.MethodPut || r.Method == http.MethodHead
	case route.KindUpload:
		return op, allowedUpload(r, op)
	default:
		return op, false
	}
}

// allowedUpload admits starting an upload without cross-repository mount
// and continuing or completing an existing one.
func allowedUpload(r *http.Request, op route.Operation) bool {
	if op.UploadID != "" {
		return r.Method == http.MethodPatch || r.Method == http.MethodPut
	}
	if r.Method != http.MethodPost {
		return false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	return err == nil && !query.Has("mount") && !query.Has("from")
}
