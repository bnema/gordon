package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/pkg/validation"
)

// previewService is a minimal interface for preview management used by the admin handler.
type previewService interface {
	List(ctx context.Context) ([]domain.PreviewRoute, error)
	Delete(ctx context.Context, name string) error
	Extend(ctx context.Context, name string, ttl time.Duration) error
}

// previewListResponse is the JSON payload returned by GET /admin/previews.
type previewListResponse struct {
	Previews []domain.PreviewRoute `json:"previews"`
}

// previewExtendRequest is the JSON body for PATCH /admin/preview/{name}.
type previewExtendRequest struct {
	TTL string `json:"ttl"` // e.g. "24h", "30m"
}

// handlePreviewList handles GET /admin/previews.
// Returns a JSON array of all active preview environments.
func (h *Handler) handlePreviewList(w http.ResponseWriter, r *http.Request, _ string) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}

	if h.previewSvc == nil {
		h.sendError(w, http.StatusServiceUnavailable, "preview service not available")
		return
	}

	previews, err := h.previewSvc.List(ctx)
	if err != nil {
		log := zerowrap.FromCtx(ctx)
		log.Error().Err(err).Msg("failed to list previews")
		h.sendError(w, http.StatusInternalServerError, "failed to list previews")
		return
	}

	if previews == nil {
		previews = []domain.PreviewRoute{}
	}

	h.sendJSON(w, http.StatusOK, previewListResponse{Previews: previews})
}

// handlePreviewAction handles DELETE and PATCH on /admin/preview/{name}.
//
//	DELETE /admin/preview/{name}       — tear down the preview
//	PATCH  /admin/preview/{name}       — extend TTL (body: {"ttl":"24h"})
func (h *Handler) handlePreviewAction(w http.ResponseWriter, r *http.Request, path string) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	if h.previewSvc == nil {
		h.sendError(w, http.StatusServiceUnavailable, "preview service not available")
		return
	}

	if !HasAccess(ctx, domain.AdminResourceConfig, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for config:write")
		return
	}

	name := strings.TrimPrefix(path, "/preview/")
	if name == "" || name == "/preview" {
		h.sendError(w, http.StatusBadRequest, "preview name required in path")
		return
	}
	if err := validation.ValidateDomainParam(name); err != nil {
		h.sendError(w, http.StatusBadRequest, "invalid domain")
		return
	}

	switch r.Method {
	case http.MethodDelete:

		if err := h.previewSvc.Delete(ctx, name); err != nil {
			log.Error().Err(err).Str("name", name).Msg("failed to delete preview")
			if errors.Is(err, domain.ErrPreviewNotFound) {
				h.sendError(w, http.StatusNotFound, "preview not found")
			} else {
				h.sendError(w, http.StatusInternalServerError, "failed to delete preview")
			}
			return
		}

		log.Info().Str("name", name).Msg("preview deleted via admin API")
		h.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})

	case http.MethodPatch:
		h.handlePreviewExtend(w, r, name)

	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handlePreviewExtend(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestSize)

	var req previewExtendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Warn().Err(err).Msg("invalid preview extend JSON")
		h.sendError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.TTL == "" {
		h.sendError(w, http.StatusBadRequest, "ttl is required")
		return
	}

	ttl, err := time.ParseDuration(req.TTL)
	if err != nil {
		h.sendError(w, http.StatusBadRequest, "invalid ttl: use Go duration format, e.g. 24h or 30m")
		return
	}

	if ttl <= 0 {
		h.sendError(w, http.StatusBadRequest, "TTL must be positive")
		return
	}
	const maxPreviewTTL = 30 * 24 * time.Hour // 30 days
	if ttl > maxPreviewTTL {
		h.sendError(w, http.StatusBadRequest, "TTL exceeds maximum of 30 days")
		return
	}

	if err := h.previewSvc.Extend(ctx, name, ttl); err != nil {
		log.Error().Err(err).Str("name", name).Dur("ttl", ttl).Msg("failed to extend preview TTL")
		if errors.Is(err, domain.ErrPreviewNotFound) {
			h.sendError(w, http.StatusNotFound, "preview not found")
		} else {
			h.sendError(w, http.StatusInternalServerError, "failed to extend preview")
		}
		return
	}

	log.Info().Str("name", name).Dur("ttl", ttl).Msg("preview TTL extended via admin API")
	h.sendJSON(w, http.StatusOK, map[string]string{"status": "extended", "name": name, "ttl": req.TTL})
}
