package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/domain"
)

func toImageResponse(image domain.ImageInfo) dto.Image {
	return dto.Image{
		Repository: image.Repository,
		Tag:        image.Tag,
		Size:       image.Size,
		Created:    image.Created,
		ID:         image.ID,
		Dangling:   image.Dangling,
	}
}

func toImagePruneResponse(report domain.ImagePruneReport) dto.ImagePruneResponse {
	return dto.ImagePruneResponse{
		Runtime: dto.RuntimePruneResult{
			DeletedCount:   report.Runtime.DeletedCount,
			SpaceReclaimed: report.Runtime.SpaceReclaimed,
		},
		Registry: dto.RegistryPruneResult{
			TagsRemoved:    report.Registry.TagsRemoved,
			BlobsRemoved:   report.Registry.BlobsRemoved,
			SpaceReclaimed: report.Registry.SpaceReclaimed,
		},
	}
}

func (h *Handler) handleImages(w http.ResponseWriter, r *http.Request, path string) {
	if path != "/images" && path != "/images/prune" {
		h.sendError(w, http.StatusNotFound, "route not found")
		return
	}

	if h.imageSvc == nil {
		h.sendError(w, http.StatusServiceUnavailable, "image service not available")
		return
	}

	switch {
	case path == "/images" && r.Method == http.MethodGet:
		h.handleImagesGet(w, r)
	case path == "/images/prune" && r.Method == http.MethodPost:
		h.handleImagesPrune(w, r)
	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleImagesGet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}

	images, err := h.imageSvc.ListImages(ctx)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "failed to list images")
		return
	}

	response := make([]dto.Image, 0, len(images))
	for _, image := range images {
		response = append(response, toImageResponse(image))
	}

	h.sendJSON(w, http.StatusOK, dto.ImagesResponse{Images: response})
}

func (h *Handler) handleImagesPrune(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	if !HasAccess(ctx, domain.AdminResourceConfig, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for config:write")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestSize)
	var req dto.ImagePruneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		log.Warn().Err(err).Msg("invalid image prune JSON")
		h.sendError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	opts := domain.DefaultImagePruneOptions()
	if req.KeepLast != nil {
		if *req.KeepLast < 0 {
			h.sendError(w, http.StatusBadRequest, "keep_last must be >= 0")
			return
		}
		opts.KeepLast = *req.KeepLast
	}
	if req.PruneDangling != nil {
		opts.PruneDangling = *req.PruneDangling
	}
	if req.PruneRegistry != nil {
		opts.PruneRegistry = *req.PruneRegistry
	}
	if !opts.PruneDangling && !opts.PruneRegistry {
		h.sendError(w, http.StatusBadRequest, "at least one prune scope must be enabled")
		return
	}

	report, err := h.imageSvc.Prune(ctx, opts)
	if err != nil {
		log.Error().Err(err).Int("keep_last", opts.KeepLast).Msg("image prune failed")
		h.sendError(w, http.StatusInternalServerError, "failed to prune images")
		return
	}

	h.sendJSON(w, http.StatusOK, toImagePruneResponse(report))
}
