package admin

import (
	"encoding/json"
	"net/http"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/domain"
)

// handleListVolumes handles GET /admin/volumes endpoint.
func (h *Handler) handleListVolumes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	if !HasAccess(ctx, domain.AdminResourceVolumes, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for volumes:read")
		return
	}

	if h.volumeSvc == nil {
		h.sendError(w, http.StatusServiceUnavailable, "volume service not available")
		return
	}

	vols, err := h.volumeSvc.ListVolumes(ctx)
	if err != nil {
		log := zerowrap.FromCtx(ctx)
		log.Error().Err(err).Msg("failed to list volumes")
		h.sendError(w, http.StatusInternalServerError, "failed to list volumes")
		return
	}

	result := make([]dto.Volume, len(vols))
	for i, v := range vols {
		result[i] = dto.Volume{
			Name:       v.Name,
			Driver:     v.Driver,
			MountPoint: v.MountPoint,
			Size:       v.Size,
			CreatedAt:  v.CreatedAt,
			InUse:      v.InUse,
			Containers: v.Containers,
			Labels:     v.Labels,
		}
	}

	h.sendJSON(w, http.StatusOK, result)
}

// handlePruneVolumes handles POST /admin/volumes/prune endpoint.
func (h *Handler) handlePruneVolumes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	if !HasAccess(ctx, domain.AdminResourceVolumes, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for volumes:write")
		return
	}

	if h.volumeSvc == nil {
		h.sendError(w, http.StatusServiceUnavailable, "volume service not available")
		return
	}

	var req dto.VolumePruneRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestSize)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	report, removed, err := h.volumeSvc.PruneVolumes(ctx, req.DryRun)
	if err != nil {
		log := zerowrap.FromCtx(ctx)
		log.Error().Err(err).Msg("failed to prune volumes")
		h.sendError(w, http.StatusInternalServerError, "failed to prune volumes")
		return
	}

	vols := make([]dto.Volume, len(removed))
	for i, v := range removed {
		vols[i] = dto.Volume{
			Name: v.Name,
			Size: v.Size,
		}
	}

	h.sendJSON(w, http.StatusOK, dto.VolumePruneResponse{
		VolumesRemoved: report.VolumesRemoved,
		SpaceReclaimed: report.SpaceReclaimed,
		Volumes:        vols,
	})
}
