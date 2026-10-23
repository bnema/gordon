package ci

import (
	"net/http"
	"strings"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/http/appapi"
	"github.com/bnema/gordon/internal/domain"
)

// ciRoute is one parsed /-/ci/ route.
type ciRoute struct {
	app    string
	key    string
	lookup bool
}

// parseCIRoute matches POST /-/ci/apps/{app}/promote and
// GET /-/ci/apps/{app}/operations/by-key/{key}.
func parseCIRoute(r *http.Request) (ciRoute, bool) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, ciPrefix), "/")
	if len(parts) < 3 || parts[0] != "apps" || domain.ValidateAppName(parts[1]) != nil {
		return ciRoute{}, false
	}
	switch {
	case len(parts) == 3 && parts[2] == "promote" && r.Method == http.MethodPost:
		return ciRoute{app: parts[1]}, true
	case len(parts) == 5 && parts[2] == "operations" && parts[3] == "by-key" &&
		r.Method == http.MethodGet && appapi.ValidIdempotencyKey(parts[4]):
		return ciRoute{app: parts[1], key: parts[4], lookup: true}, true
	default:
		return ciRoute{}, false
	}
}

// serveCI serves the promotion routes after the allowlist and CIAuth.
func (h *Handler) serveCI(w http.ResponseWriter, r *http.Request) {
	rt, ok := parseCIRoute(r)
	if !ok {
		writeNotFound(w)
		return
	}
	r, c, ok := h.requireCaller(w, r, false)
	if !ok {
		return
	}
	if rt.lookup {
		h.handleLookup(w, r, c, rt)
		return
	}
	h.handlePromote(w, r, c, rt.app)
}

// handlePromote runs exactly one digest in one service as the ci-access
// principal. Authorization against the promotion policies happens in the
// usecase, exactly as for an admin caller; a CI caller is never the local
// owner.
func (h *Handler) handlePromote(w http.ResponseWriter, r *http.Request, c caller, app string) {
	req, err := appapi.DecodePromoteRequest(w, r)
	if err != nil {
		appapi.WriteError(w, err)
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !appapi.ValidIdempotencyKey(key) {
		writeJSON(w, http.StatusBadRequest, dto.AppError{Error: "invalid-request", Message: "valid Idempotency-Key header required"})
		return
	}
	op, err := h.deps.Apps.Promote(r.Context(), appapi.PromoteInput(app, key, req, c.principal, false))
	status, respond := appapi.PromoteOutcome(op, err)
	if !respond {
		appapi.WriteError(w, err)
		return
	}
	writeJSON(w, status, ciMutationResponse(appapi.MutationResponse(r.Context(), h.deps.Apps, app, op, false)))
}

// handleLookup reports an operation the principal submitted. A principal
// with no policy for the app, or a key recorded for anyone else, is told
// the operation does not exist.
func (h *Handler) handleLookup(w http.ResponseWriter, r *http.Request, c caller, rt ciRoute) {
	if !h.deps.Policies().HasAppPolicy(c.principal, rt.app) {
		appapi.WriteError(w, domain.ErrAppOperationNotFound)
		return
	}
	op, err := h.deps.Apps.PrincipalOperationByKey(r.Context(), rt.app, rt.key, c.principal)
	if err != nil {
		appapi.WriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, appapi.OperationResponse(rt.app, op, false))
}

// ciMutationResponse drops the retained resource names a CI identity has no
// need to learn; the operation, results and effective state are unchanged.
func ciMutationResponse(resp dto.AppDeployResponse) dto.AppDeployResponse {
	resp.Retained = nil
	return resp
}
