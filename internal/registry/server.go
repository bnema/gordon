package registry

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"gordon/internal/config"
	"gordon/internal/events"
	"gordon/internal/middleware"

	"github.com/rs/zerolog/log"
)

type Server struct {
	config   *config.Config
	mux      *http.ServeMux
	storage  Storage
	eventBus events.EventBus
}

func NewServer(cfg *config.Config, eventBus events.EventBus) (*Server, error) {
	// Initialize storage
	registryPath := cfg.Server.DataDir + "/registry"
	log.Debug().Str("data_dir", cfg.Server.DataDir).Str("registry_path", registryPath).Msg("Initializing registry storage")

	storage, err := NewFilesystemStorage(registryPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	s := &Server{
		config:   cfg,
		mux:      http.NewServeMux(),
		storage:  storage,
		eventBus: eventBus,
	}
	s.setupRoutes()
	return s, nil
}

func (s *Server) setupRoutes() {
	// Docker Registry API v2 endpoints using Go 1.22+ ServeMux patterns
	s.mux.HandleFunc("GET /v2/", s.handleBase)
	s.mux.HandleFunc("HEAD /v2/{name}/manifests/{reference}", s.handleGetManifest)
	s.mux.HandleFunc("GET /v2/{name}/manifests/{reference}", s.handleGetManifest)
	s.mux.HandleFunc("PUT /v2/{name}/manifests/{reference}", s.handlePutManifest)
	s.mux.HandleFunc("HEAD /v2/{name}/blobs/{digest}", s.handleGetBlob)
	s.mux.HandleFunc("GET /v2/{name}/blobs/{digest}", s.handleGetBlob)
	s.mux.HandleFunc("POST /v2/{name}/blobs/uploads/", s.handleStartBlobUpload)
	s.mux.HandleFunc("PATCH /v2/{name}/blobs/uploads/{uuid}", s.handleBlobUpload)
	s.mux.HandleFunc("PUT /v2/{name}/blobs/uploads/{uuid}", s.handleBlobUpload)
	s.mux.HandleFunc("GET /v2/{name}/tags/list", s.handleListTags)
}

func (s *Server) handleBase(w http.ResponseWriter, r *http.Request) {
	// For Docker clients, we need to respond with a 200 OK to this endpoint
	// even if authentication is required. The client will then check the
	// WWW-Authenticate header.
	// see: https://docs.docker.com/registry/spec/api/#base
	//
	// Podman, as of v4.4.1, will not send credentials in the initial /v2/
	// request. It expects a 401 Unauthorized to then send credentials.
	//
	// To support both, we'll check for the "Docker-Client" user agent.
	// For now, we will just return a 200 OK to all clients.
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGetManifest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	reference := r.PathValue("reference")

	log.Debug().Str("name", name).Str("reference", reference).Msg("GET manifest")

	manifest, contentType, err := s.storage.GetManifest(name, reference)
	if err != nil {
		log.Warn().Err(err).Str("name", name).Str("reference", reference).Msg("Manifest not found")
		http.NotFound(w, r)
		return
	}

	log.Debug().Str("name", name).Str("reference", reference).Str("content_type", contentType).Msg("GET manifest - serving")

	// Return the appropriate content type based on what was stored
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(manifest)))
	w.WriteHeader(http.StatusOK)

	if r.Method == "GET" {
		_, _ = w.Write(manifest)
	}
}

func (s *Server) handlePutManifest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	reference := r.PathValue("reference")

	contentType := r.Header.Get("Content-Type")
	log.Debug().Str("name", name).Str("reference", reference).Str("content_type", contentType).Msg("PUT manifest")

	if contentType == "" {
		log.Warn().Str("name", name).Str("reference", reference).Msg("Content-Type header missing for PUT manifest")
		http.Error(w, "Content-Type header required", http.StatusBadRequest)
		return
	}

	// Read manifest data
	data, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error().Err(err).Msg("Failed to read manifest data")
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// Store manifest
	if err := s.storage.PutManifest(name, reference, contentType, data); err != nil {
		log.Error().Err(err).Str("name", name).Str("reference", reference).Msg("Failed to store manifest")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Calculate digest for Docker-Content-Digest header
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", name, reference))
	w.WriteHeader(http.StatusCreated)

	// Emit image pushed event
	s.eventBus.Publish(events.ImagePushed, events.ImagePushedPayload{
		Name:      name,
		Reference: reference,
		Manifest:  data,
	})
}

func (s *Server) handleGetBlob(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	digest := r.PathValue("digest")

	log.Debug().Str("name", name).Str("digest", digest).Msg("GET blob")

	path, err := s.storage.GetBlobPath(digest)
	if err != nil {
		log.Warn().Err(err).Str("name", name).Str("digest", digest).Msg("Blob not found")
		http.NotFound(w, r)
		return
	}

	// Serve the file directly
	http.ServeFile(w, r, path)
}

func (s *Server) handleStartBlobUpload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	log.Debug().Str("name", name).Msg("Starting blob upload")

	// Generate a unique upload ID
	uuid, err := s.storage.StartBlobUpload(name)
	if err != nil {
		log.Error().Err(err).Msg("Failed to start blob upload")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Set headers for the response
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uuid))
	w.Header().Set("Range", "0-0") // Indicate that no bytes have been received yet
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleBlobUpload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	uuid := r.PathValue("uuid")
	digest := r.URL.Query().Get("digest")

	log.Debug().
		Str("name", name).
		Str("uuid", uuid).
		Str("digest", digest).
		Str("method", r.Method).
		Msg("Handling blob upload chunk")

	// Read the chunk from the request body
	chunk, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error().Err(err).Msg("Failed to read blob chunk")
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// Append the chunk to the upload
	length, err := s.storage.AppendBlobChunk(name, uuid, chunk)
	if err != nil {
		log.Error().Err(err).Msg("Failed to append blob chunk")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// If this is the final chunk (PUT request with digest), finalize the upload
	if r.Method == "PUT" && digest != "" {
		if err := s.storage.FinishBlobUpload(uuid, digest); err != nil {
			log.Error().Err(err).Str("digest", digest).Msg("Failed to finalize blob upload")
			// The upload is invalid, delete it
			_ = s.storage.CancelBlobUpload(uuid)
			http.Error(w, "Bad Request: digest mismatch", http.StatusBadRequest)
			return
		}

		// Respond with 201 Created
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

func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	log.Debug().Str("name", name).Msg("Listing tags")

	tags, err := s.storage.ListTags(name)
	if err != nil {
		log.Warn().Err(err).Str("name", name).Msg("Tag not found")
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Construct the response JSON
	response := fmt.Sprintf(`{"name":"%s","tags":%s}`, name, tags)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(response)))
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(response))
}

func (s *Server) Start(ctx context.Context) error {
	addr := ":" + strconv.Itoa(s.config.Server.RegistryPort)

	// Build middleware chain
	middlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery,
		middleware.RequestLogger,
	}

	// Add registry authentication if enabled
	if s.config.RegistryAuth.Enabled {
		middlewares = append(middlewares, middleware.RegistryAuth(s.config.RegistryAuth.Username, s.config.RegistryAuth.Password))
	}

	// Wrap the mux with middleware
	handler := middleware.Chain(middlewares...)(s.mux)

	server := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	log.Info().Str("address", addr).Msg("Registry server starting")

	// Start server in a goroutine
	errChan := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	// Wait for context cancellation or server error
	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		log.Info().Msg("Registry server shutting down...")
		return server.Shutdown(context.Background())
	}
}
