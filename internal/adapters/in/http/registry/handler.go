// Package registry implements the HTTP adapter for the registry API.
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/pkg/manifest"
	"github.com/bnema/gordon/pkg/validation"
)

const (
	// MaxManifestSize limits manifest uploads to 10MB.
	MaxManifestSize = 10 * 1024 * 1024
	// MaxBlobChunkSize limits individual blob chunks to 100MB.
	MaxBlobChunkSize = 100 * 1024 * 1024
)

// Handler implements the HTTP handler for Docker Registry API v2.
type Handler struct {
	registrySvc in.RegistryService
	log         zerowrap.Logger
}

// NewHandler creates a new registry HTTP handler.
func NewHandler(
	registrySvc in.RegistryService,
	log zerowrap.Logger,
) *Handler {
	return &Handler{
		registrySvc: registrySvc,
		log:         log,
	}
}

// RegisterRoutes registers the registry routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v2/", h.handleRegistryRoutes)
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handleRegistryRoutes(w, r)
}

func (h *Handler) handleRegistryRoutes(w http.ResponseWriter, r *http.Request) {
	ctx := zerowrap.CtxWithFields(r.Context(), map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "http",
		zerowrap.FieldHandler: "registry",
		zerowrap.FieldMethod:  r.Method,
		zerowrap.FieldPath:    r.URL.Path,
	})
	r = r.WithContext(ctx)

	path := r.URL.Path

	// Route manifest operations: /v2/{name}/manifests/{reference}
	if strings.Contains(path, "/manifests/") {
		h.handleManifestRoutes(w, r)
		return
	}

	// Route blob operations: /v2/{name}/blobs/{digest}
	if strings.Contains(path, "/blobs/") && !strings.Contains(path, "/uploads/") {
		h.handleBlobRoutes(w, r)
		return
	}

	// Route blob upload operations: /v2/{name}/blobs/uploads/
	if strings.Contains(path, "/blobs/uploads/") {
		h.handleBlobUploadRoutes(w, r)
		return
	}

	// Route tag list operations: /v2/{name}/tags/list
	if strings.Contains(path, "/tags/list") {
		h.handleTagListRoutes(w, r)
		return
	}

	// Base endpoint: /v2/
	if path == "/v2/" {
		h.handleBase(w, r)
		return
	}
	h.sendRegistryError(w, http.StatusNotFound, "NOT_FOUND", "route not found")

}

func (h *Handler) handleManifestRoutes(w http.ResponseWriter, r *http.Request) {
	// Parse path: /v2/{name}/manifests/{reference}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v2/"), "/")
	if len(parts) < 3 || parts[len(parts)-2] != "manifests" {
		h.sendRegistryError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		return
	}

	reference := parts[len(parts)-1]
	name := strings.Join(parts[:len(parts)-2], "/")

	// Validate inputs to prevent path traversal
	if err := validation.ValidateRepositoryName(name); err != nil {
		h.sendRegistryError(w, http.StatusBadRequest, "NAME_INVALID", err.Error())
		return
	}
	if err := validation.ValidateReference(reference); err != nil {
		h.sendRegistryError(w, http.StatusBadRequest, "TAG_INVALID", err.Error())
		return
	}

	r.SetPathValue("name", name)
	r.SetPathValue("reference", reference)

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
	// Parse path: /v2/{name}/blobs/{digest}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v2/"), "/")
	if len(parts) < 3 || parts[len(parts)-2] != "blobs" {
		h.sendRegistryError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		return
	}

	digest := parts[len(parts)-1]
	name := strings.Join(parts[:len(parts)-2], "/")

	// Validate inputs to prevent path traversal
	if err := validation.ValidateRepositoryName(name); err != nil {
		h.sendRegistryError(w, http.StatusBadRequest, "NAME_INVALID", err.Error())
		return
	}
	if err := validation.ValidateDigest(digest); err != nil {
		h.sendRegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", err.Error())
		return
	}

	r.SetPathValue("name", name)
	r.SetPathValue("digest", digest)

	switch r.Method {
	case "HEAD", "GET":
		h.handleGetBlob(w, r)
	default:
		h.sendRegistryError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (h *Handler) handleBlobUploadRoutes(w http.ResponseWriter, r *http.Request) {
	// Parse path: /v2/{name}/blobs/uploads/{uuid?}
	path := strings.TrimPrefix(r.URL.Path, "/v2/")
	uploadIndex := strings.Index(path, "/blobs/uploads/")
	if uploadIndex == -1 {
		h.sendRegistryError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		return
	}

	name := path[:uploadIndex]
	uploadPart := path[uploadIndex+15:] // len("/blobs/uploads/") = 15

	// Validate repository name to prevent path traversal
	if err := validation.ValidateRepositoryName(name); err != nil {
		h.sendRegistryError(w, http.StatusBadRequest, "NAME_INVALID", err.Error())
		return
	}

	r.SetPathValue("name", name)

	if uploadPart == "" {
		// POST /v2/{name}/blobs/uploads/
		switch r.Method {
		case "POST":
			h.handleStartBlobUpload(w, r)
		default:
			h.sendRegistryError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		}
	} else {
		// Validate UUID to prevent path traversal
		if err := validation.ValidateUUID(uploadPart); err != nil {
			h.sendRegistryError(w, http.StatusBadRequest, "BLOB_UPLOAD_INVALID", err.Error())
			return
		}
		// PATCH/PUT /v2/{name}/blobs/uploads/{uuid}
		r.SetPathValue("uuid", uploadPart)
		switch r.Method {
		case "PATCH", "PUT":
			h.handleBlobUpload(w, r)
		default:
			h.sendRegistryError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		}
	}
}

func (h *Handler) handleTagListRoutes(w http.ResponseWriter, r *http.Request) {
	// Parse path: /v2/{name}/tags/list
	path := strings.TrimPrefix(r.URL.Path, "/v2/")
	if !strings.HasSuffix(path, "/tags/list") {
		h.sendRegistryError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		return
	}

	name := strings.TrimSuffix(path, "/tags/list")

	// Validate repository name to prevent path traversal
	if err := validation.ValidateRepositoryName(name); err != nil {
		h.sendRegistryError(w, http.StatusBadRequest, "NAME_INVALID", err.Error())
		return
	}

	r.SetPathValue("name", name)

	switch r.Method {
	case "GET":
		h.handleListTags(w, r)
	default:
		h.sendRegistryError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (h *Handler) handleBase(w http.ResponseWriter, r *http.Request) {
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

	path, err := h.registrySvc.GetBlobPath(ctx, digest)
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

	// Limit blob chunk size to prevent memory exhaustion
	r.Body = http.MaxBytesReader(w, r.Body, MaxBlobChunkSize)

	// Read the chunk from the request body
	chunk, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			log.Warn().Int64("max_size", MaxBlobChunkSize).Msg("blob chunk too large")
			h.sendRegistryError(w, http.StatusRequestEntityTooLarge, "SIZE_INVALID", "blob chunk exceeds maximum size")
			return
		}
		log.Error().Err(err).Msg("failed to read blob chunk")
		h.sendRegistryError(w, http.StatusBadRequest, "BLOB_UPLOAD_INVALID", "invalid blob chunk")
		return
	}

	// Append the chunk to the upload
	length, err := h.registrySvc.AppendBlobChunk(ctx, name, uuid, chunk)
	if err != nil {
		log.Error().Err(err).Msg("failed to append blob chunk")
		h.sendRegistryError(w, http.StatusInternalServerError, "BLOB_UPLOAD_UNKNOWN", "failed to append blob chunk")
		return
	}

	// If this is the final chunk (PUT request with digest), finalize the upload
	if r.Method == "PUT" && digest != "" {
		if err := h.registrySvc.FinishUpload(ctx, uuid, digest); err != nil {
			log.Error().Err(err).Str("digest", digest).Msg("failed to finalize blob upload")
			_ = h.registrySvc.CancelUpload(ctx, uuid)
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
	w.Header().Set("Range", fmt.Sprintf("0-%d", length-1))
	w.WriteHeader(http.StatusAccepted)
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
