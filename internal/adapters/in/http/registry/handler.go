// Package registry implements the HTTP adapter for the registry API.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/http/registry/route"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/pkg/manifest"
	"github.com/bnema/gordon/pkg/validation"
)

const (
	// MaxManifestSize limits manifest uploads to 10MB.
	MaxManifestSize = 10 * 1024 * 1024
	// DefaultMaxBlobChunkSize is the default maximum size for a single blob upload chunk.
	// Kept at 95MB to stay within Cloudflare's 100MB per-request limit.
	// Users who need larger chunks can configure it explicitly via max_blob_chunk_size.
	DefaultMaxBlobChunkSize = 95 * 1024 * 1024
	// DefaultMaxBlobSize is the default maximum cumulative size for a blob upload.
	DefaultMaxBlobSize = 1024 * 1024 * 1024
)

// blobLimits holds consistent blob size limits.
type blobLimits struct {
	MaxChunkSize int64
	MaxBlobSize  int64
}

// Handler implements the HTTP handler for Docker Registry API v2.
type Handler struct {
	registrySvc in.RegistryService
	log         zerowrap.Logger
	blobLimits  atomic.Value // stores *blobLimits
}

// NewHandler creates a new registry HTTP handler.
func NewHandler(
	registrySvc in.RegistryService,
	log zerowrap.Logger,
	maxBlobChunkSize int64,
	maxBlobSize ...int64,
) *Handler {
	if maxBlobChunkSize <= 0 {
		maxBlobChunkSize = DefaultMaxBlobChunkSize
	}
	blobSizeLimit := int64(DefaultMaxBlobSize)
	if len(maxBlobSize) > 0 && maxBlobSize[0] > 0 {
		blobSizeLimit = maxBlobSize[0]
	}

	h := &Handler{
		registrySvc: registrySvc,
		log:         log,
	}
	h.blobLimits.Store(&blobLimits{
		MaxChunkSize: maxBlobChunkSize,
		MaxBlobSize:  blobSizeLimit,
	})
	return h
}

// UpdateBlobLimits updates request-size limits used by live upload handlers.
func (h *Handler) UpdateBlobLimits(maxBlobChunkSize, maxBlobSize int64) {
	if maxBlobChunkSize <= 0 {
		maxBlobChunkSize = DefaultMaxBlobChunkSize
	}
	if maxBlobSize <= 0 {
		maxBlobSize = DefaultMaxBlobSize
	}
	h.blobLimits.Store(&blobLimits{
		MaxChunkSize: maxBlobChunkSize,
		MaxBlobSize:  maxBlobSize,
	})
}

// RegisterRoutes registers the registry routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v2/", h.handleRegistryRoutes)
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handleRegistryRoutes(w, r)
}

// handleRegistryRoutes parses the request path once and dispatches on the
// resulting operation. Authorization middleware parses the same path with
// the same parser, so the repository that is authorized is exactly the
// repository the handler accesses.
func (h *Handler) handleRegistryRoutes(w http.ResponseWriter, r *http.Request) {
	ctx := zerowrap.CtxWithFields(r.Context(), map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "http",
		zerowrap.FieldHandler: "registry",
		zerowrap.FieldMethod:  r.Method,
		zerowrap.FieldPath:    r.URL.Path,
	})
	r = r.WithContext(ctx)

	op, err := route.Parse(r.URL.Path)
	if err != nil {
		var parseErr *route.ParseError
		if errors.As(err, &parseErr) {
			h.sendRegistryError(w, http.StatusBadRequest, parseErr.Code, parseErr.Error())
			return
		}
		h.sendRegistryError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		return
	}

	// Methods are validated per operation kind after the parsed values are
	// installed, so handlers only read the path values the parser produced.
	switch op.Kind {
	case route.KindBase:
		h.handleBase(w, r)
	case route.KindManifest:
		r.SetPathValue("name", op.Repository)
		r.SetPathValue("reference", op.Reference)
		h.handleManifestRoutes(w, r)
	case route.KindBlob:
		r.SetPathValue("name", op.Repository)
		r.SetPathValue("digest", op.Digest)
		h.handleBlobRoutes(w, r)
	case route.KindUpload:
		r.SetPathValue("name", op.Repository)
		if op.UploadID != "" {
			r.SetPathValue("uuid", op.UploadID)
		}
		h.handleBlobUploadRoutes(w, r)
	case route.KindTagList:
		r.SetPathValue("name", op.Repository)
		h.handleTagListRoutes(w, r)
	default:
		h.sendRegistryError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
	}
}

func (h *Handler) handleManifestRoutes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "HEAD", "GET":
		h.handleGetManifest(w, r)
	case "PUT":
		h.handlePutManifest(w, r)
	default:
		h.sendRegistryError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (h *Handler) handleBlobRoutes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "HEAD", "GET":
		h.handleGetBlob(w, r)
	default:
		h.sendRegistryError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (h *Handler) handleBlobUploadRoutes(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("uuid") == "" {
		// POST /v2/{name}/blobs/uploads/
		switch r.Method {
		case "POST":
			h.handleStartBlobUpload(w, r)
		default:
			h.sendRegistryError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		}
		return
	}
	// PATCH/PUT /v2/{name}/blobs/uploads/{uuid}
	switch r.Method {
	case "PATCH", "PUT":
		h.handleBlobUpload(w, r)
	default:
		h.sendRegistryError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (h *Handler) handleTagListRoutes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		h.handleListTags(w, r)
	default:
		h.sendRegistryError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (h *Handler) handleBase(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(http.StatusOK)
}

// sendRegistryError sends a Docker Registry V2 formatted error response.
func (h *Handler) sendRegistryError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(dto.RegistryErrorResponse{
		Errors: []dto.RegistryErrorItem{{
			Code:    code,
			Message: message,
		}},
	})
}

func (h *Handler) handleGetManifest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)
	name := r.PathValue("name")
	reference := r.PathValue("reference")

	log.Debug().Str("name", name).Str("reference", reference).Msg("GET manifest")

	manifestData, err := h.registrySvc.GetManifest(ctx, name, reference)
	if err != nil {
		log.Warn().Err(err).Str("name", name).Str("reference", reference).Msg("manifest not found")
		h.sendRegistryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest not found")
		return
	}

	w.Header().Set("Content-Type", manifestData.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(manifestData.Data)))
	w.WriteHeader(http.StatusOK)

	if r.Method == "GET" {
		_, _ = w.Write(manifestData.Data)
	}
}

func (h *Handler) handlePutManifest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)
	name := r.PathValue("name")
	reference := r.PathValue("reference")

	contentType := r.Header.Get("Content-Type")
	log.Debug().Str("name", name).Str("reference", reference).Str("content_type", contentType).Msg("PUT manifest")

	if contentType == "" {
		log.Warn().Str("name", name).Str("reference", reference).Msg("Content-Type header missing")
		h.sendRegistryError(w, http.StatusBadRequest, "MANIFEST_INVALID", "Content-Type header required")
		return
	}

	// Limit manifest size to prevent memory exhaustion
	r.Body = http.MaxBytesReader(w, r.Body, MaxManifestSize)

	// Read manifest data
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			log.Warn().Int64("max_size", MaxManifestSize).Msg("manifest too large")
			h.sendRegistryError(w, http.StatusRequestEntityTooLarge, "SIZE_INVALID", "manifest exceeds maximum size")
			return
		}
		log.Error().Err(err).Msg("failed to read manifest data")
		h.sendRegistryError(w, http.StatusBadRequest, "MANIFEST_INVALID", "invalid manifest data")
		return
	}

	// Parse manifest annotations
	annotations, err := manifest.ParseManifestAnnotations(data, contentType)
	if err != nil {
		log.Warn().Err(err).Str("name", name).Str("reference", reference).Msg("failed to parse manifest annotations")
		annotations = map[string]string{}
	}

	// Store manifest via service
	manifestObj := &domain.Manifest{
		Name:        name,
		Reference:   reference,
		ContentType: contentType,
		Data:        data,
		Annotations: annotations,
	}

	digest, err := h.registrySvc.PutManifest(ctx, manifestObj)
	if err != nil {
		log.Error().Err(err).Str("name", name).Str("reference", reference).Msg("failed to store manifest")
		if errors.Is(err, domain.ErrDigestMismatch) || errors.Is(err, domain.ErrInvalidDigest) {
			h.sendRegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", "manifest digest does not match content")
			return
		}
		if errors.Is(err, domain.ErrManifestBlobUnknown) {
			h.sendRegistryError(w, http.StatusBadRequest, "MANIFEST_BLOB_UNKNOWN", "manifest references unknown content")
			return
		}
		h.sendRegistryError(w, http.StatusInternalServerError, "MANIFEST_INVALID", "failed to store manifest")
		return
	}

	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", name, reference))
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) handleGetBlob(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)
	name := r.PathValue("name")
	digest := r.PathValue("digest")

	log.Debug().Str("name", name).Str("digest", digest).Msg("GET blob")

	path, err := h.registrySvc.GetBlobPath(ctx, name, digest)
	if err != nil {
		log.Warn().Err(err).Str("name", name).Str("digest", digest).Msg("blob not found")
		h.sendRegistryError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found")
		return
	}

	// Serve the file directly
	http.ServeFile(w, r, path)
}

func (h *Handler) handleStartBlobUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)
	name := r.PathValue("name")

	log.Debug().Str("name", name).Msg("starting blob upload")

	uuid, err := h.registrySvc.StartUpload(ctx, name)
	if err != nil {
		log.Error().Err(err).Msg("failed to start blob upload")
		h.sendRegistryError(w, http.StatusInternalServerError, "BLOB_UPLOAD_UNKNOWN", "failed to start upload")
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uuid))
	w.Header().Set("Range", "0-0")
	w.Header().Set("Docker-Upload-UUID", uuid)
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) handleBlobUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)
	name := r.PathValue("name")
	uuid := r.PathValue("uuid")
	digest := r.URL.Query().Get("digest")

	log.Debug().
		Str("name", name).
		Str("uuid", uuid).
		Str("digest", digest).
		Str(zerowrap.FieldMethod, r.Method).
		Msg("handling blob upload chunk")

	// Validate digest if provided (for PUT finalization)
	if digest != "" {
		if err := validation.ValidateDigest(digest); err != nil {
			h.sendRegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", err.Error())
			return
		}
	}

	// Load a consistent snapshot of both blob limits atomically
	limits := h.blobLimits.Load().(*blobLimits)
	maxBlobChunkSize := limits.MaxChunkSize
	maxBlobSize := limits.MaxBlobSize

	// Limit blob chunk size to prevent excessive uploads.
	// MaxBytesReader wraps the body so the downstream io.Copy stops at the limit.
	r.Body = http.MaxBytesReader(w, r.Body, maxBlobChunkSize)

	// Stream the body directly to storage — memory usage is bounded to a small
	// copy buffer (~32KB) regardless of chunk size.
	length, err := h.registrySvc.AppendBlobChunk(ctx, name, uuid, r.Body, r.ContentLength, maxBlobSize)
	if err != nil {
		if errors.Is(err, domain.ErrUploadNotFound) {
			log.Warn().Str("uuid", uuid).Msg("blob upload not found for repository")
			h.sendRegistryError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "blob upload unknown")
			return
		}
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			log.Warn().Int64("max_size", maxBlobChunkSize).Msg("blob chunk too large")
			if !h.cancelUploadAfterError(w, ctx, name, uuid, "failed to cancel oversized blob upload") {
				return
			}
			h.sendRegistryError(w, http.StatusRequestEntityTooLarge, "SIZE_INVALID", "blob chunk exceeds maximum size")
			return
		}
		if errors.Is(err, domain.ErrBlobSizeExceeded) {
			log.Warn().Int64("max_size", maxBlobSize).Msg("blob upload too large")
			if !h.cancelUploadAfterError(w, ctx, name, uuid, "failed to cancel oversized blob upload") {
				return
			}
			h.sendRegistryError(w, http.StatusRequestEntityTooLarge, "SIZE_INVALID", "blob exceeds maximum size")
			return
		}
		log.Error().Err(err).Msg("failed to append blob chunk")
		h.sendRegistryError(w, http.StatusInternalServerError, "BLOB_UPLOAD_UNKNOWN", "failed to append blob chunk")
		return
	}

	// If this is the final chunk (PUT request with digest), finalize the upload
	if r.Method == "PUT" && digest != "" {
		if err := h.registrySvc.FinishUpload(ctx, name, uuid, digest); err != nil {
			log.Error().Err(err).Str("digest", digest).Msg("failed to finalize blob upload")
			if errors.Is(err, domain.ErrUploadNotFound) {
				h.sendRegistryError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "blob upload unknown")
				return
			}
			if !h.cancelUploadAfterError(w, ctx, name, uuid, "failed to cancel invalid blob upload") {
				return
			}
			h.sendRegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", "digest mismatch")
			return
		}

		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusCreated)
		return
	}

	// For PATCH requests, respond with 202 Accepted
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uuid))
	if length > 0 {
		w.Header().Set("Range", fmt.Sprintf("0-%d", length-1))
	}
	w.Header().Set("Docker-Upload-UUID", uuid)
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) cancelUploadAfterError(w http.ResponseWriter, ctx context.Context, name, uuid, message string) bool {
	if err := h.registrySvc.CancelUpload(ctx, name, uuid); err != nil {
		log := zerowrap.FromCtx(ctx)
		log.Error().Err(err).Str("uuid", uuid).Msg(message)
		h.sendRegistryError(w, http.StatusInternalServerError, "UNKNOWN", "failed to cancel upload")
		return false
	}
	return true
}

func (h *Handler) handleListTags(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)
	name := r.PathValue("name")

	log.Debug().Str("name", name).Msg("listing tags")

	tags, err := h.registrySvc.ListTags(ctx, name)
	if err != nil {
		log.Warn().Err(err).Str("name", name).Msg("tags not found")
		h.sendRegistryError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}

	response := dto.TagListResponse{
		Name: name,
		Tags: tags,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Error().Err(err).Str("name", name).Msg("failed to encode tags")
	}
}
