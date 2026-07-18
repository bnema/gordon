package admin

import (
	"context"
	"net/http"

	"github.com/bnema/gordon/internal/domain"
)

// handleMigration keeps migration transport policy at the adapter boundary.
// Callbacks return sanitized report/checkpoint DTO-shaped values only; errors
// are intentionally not reflected because they may originate in providers.
func (h *Handler) handleMigration(w http.ResponseWriter, r *http.Request, method, action string, operation func(context.Context) (any, error)) {
	if r.Method != method {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !HasAccess(r.Context(), domain.AdminResourceConfig, action) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for config migration")
		return
	}
	if operation == nil {
		h.sendError(w, http.StatusServiceUnavailable, "migration service unavailable")
		return
	}
	result, err := operation(r.Context())
	if err != nil {
		h.sendError(w, http.StatusConflict, "migration operation cannot proceed")
		return
	}
	h.sendJSON(w, http.StatusOK, result)
}
