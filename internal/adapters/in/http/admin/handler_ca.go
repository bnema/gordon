package admin

import (
	"net/http"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/domain"
)

const caDisabledMessage = "internal TLS is disabled on this server (no TLS-capable entrypoint configured)"

func (h *Handler) handleCA(w http.ResponseWriter, r *http.Request) {
	if !HasAccess(r.Context(), domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.caSvc == nil {
		h.sendError(w, http.StatusNotFound, caDisabledMessage)
		return
	}
	h.sendJSON(w, http.StatusOK, dto.CAResponse{
		RootCN:             h.caSvc.RootCommonName(),
		Fingerprint:        h.caSvc.RootFingerprint(),
		IntermediateExpiry: h.caSvc.IntermediateExpiresAt().UTC(),
		RootPEM:            string(h.caSvc.RootCertificate()),
	})
}
