package admin

import (
	"net/http"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/domain"
)

func (h *Handler) handleTrafficStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}
	if h.trafficSvc == nil {
		h.sendJSON(w, http.StatusOK, dto.TrafficStatusResponse{LastReloadStatus: "unavailable"})
		return
	}
	h.sendJSON(w, http.StatusOK, dto.TrafficStatusFromDomain(h.trafficSvc.Status()))
}
