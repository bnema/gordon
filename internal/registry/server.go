package registry

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
	"gordon/internal/config"
	"gordon/internal/middleware"
)

type Server struct {
	config  *config.Config
	mux     *http.ServeMux
	storage Storage
}

func NewServer(cfg *config.Config) (*Server, error) {
	// Initialize storage
	storage, err := NewFilesystemStorage(cfg.Server.DataDir + "/registry")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	s := &Server{
		config:  cfg,
		mux:     http.NewServeMux(),
		storage: storage,
	}
	s.setupRoutes()
	return s, nil
}

func (s *Server) setupRoutes() {
	// Docker Registry API v2 endpoints using Go 1.22+ ServeMux patterns
	
	// Registry info endpoint
	s.mux.HandleFunc("GET /v2/", s.handleRegistryInfo)
	
	// Since Go's ServeMux requires wildcards to be at the end, we need a different approach
	// Use a catch-all handler for /v2/ paths and route internally
	s.mux.HandleFunc("/v2/", s.handleV2Routes)
}

func (s *Server) handleV2Routes(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	method := r.Method
	
	// Route based on path pattern and method
	switch {
	case path == "/v2/" && method == "GET":
		s.handleRegistryInfo(w, r)
	case method == "GET" && matches(path, "/v2/.*/manifests/.*"):
		s.handleGetManifest(w, r)
	case method == "PUT" && matches(path, "/v2/.*/manifests/.*"):
		s.handlePutManifest(w, r)
	case method == "GET" && matches(path, "/v2/.*/blobs/.*"):
		s.handleGetBlob(w, r)
	case method == "POST" && matches(path, "/v2/.*/blobs/uploads/"):
		s.handleStartBlobUpload(w, r)
	case (method == "PUT" || method == "PATCH") && matches(path, "/v2/.*/blobs/uploads/.*"):
		s.handleBlobUpload(w, r)
	case method == "GET" && matches(path, "/v2/.*/tags/list"):
		s.handleListTags(w, r)
	default:
		http.NotFound(w, r)
	}
}

// Simple pattern matching helper
func matches(path, pattern string) bool {
	// Convert pattern to regex-like matching
	// For now, just check if the pattern structure matches
	if pattern == "/v2/.*/manifests/.*" {
		return len(path) > 4 && path[:4] == "/v2/" && 
			   len(path) > 14 && path[len(path)-10:] != "/manifests/" &&
			   contains(path, "/manifests/")
	}
	if pattern == "/v2/.*/blobs/.*" {
		return len(path) > 4 && path[:4] == "/v2/" && contains(path, "/blobs/")
	}
	if pattern == "/v2/.*/blobs/uploads/" {
		return len(path) > 4 && path[:4] == "/v2/" && 
			   contains(path, "/blobs/uploads/") && path[len(path)-1:] == "/"
	}
	if pattern == "/v2/.*/blobs/uploads/.*" {
		return len(path) > 4 && path[:4] == "/v2/" && contains(path, "/blobs/uploads/")
	}
	if pattern == "/v2/.*/tags/list" {
		return len(path) > 4 && path[:4] == "/v2/" && 
			   len(path) > 10 && path[len(path)-10:] == "/tags/list"
	}
	return false
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Path parsing helpers
func (s *Server) parseManifestPath(path string) (name, reference string) {
	// Expected format: /v2/{name}/manifests/{reference}
	parts := strings.Split(path, "/")
	if len(parts) >= 5 && parts[3] == "manifests" {
		name = parts[2]
		reference = parts[4]
	}
	return
}

func (s *Server) parseBlobPath(path string) (name, digest string) {
	// Expected format: /v2/{name}/blobs/{digest}
	parts := strings.Split(path, "/")
	if len(parts) >= 5 && parts[3] == "blobs" {
		name = parts[2]
		digest = parts[4]
	}
	return
}

func (s *Server) parseUploadPath(path string) (name, uuid string) {
	// Expected format: /v2/{name}/blobs/uploads/{uuid}
	parts := strings.Split(path, "/")
	if len(parts) >= 6 && parts[3] == "blobs" && parts[4] == "uploads" {
		name = parts[2]
		if len(parts) > 5 {
			uuid = parts[5]
		}
	}
	return
}

func (s *Server) parseTagsPath(path string) string {
	// Expected format: /v2/{name}/tags/list
	parts := strings.Split(path, "/")
	if len(parts) >= 4 && parts[3] == "tags" {
		return parts[2]
	}
	return ""
}

func (s *Server) Start(ctx context.Context) error {
	addr := ":" + strconv.Itoa(s.config.Server.RegistryPort)
	
	// Build middleware chain
	middlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery,
		middleware.RequestLogger,
	}
	
	// Add registry authentication if enabled
	if s.config.Auth.Enabled && s.config.Auth.RegistryAuth {
		if s.config.Auth.Username != "" && s.config.Auth.Password != "" {
			middlewares = append(middlewares, middleware.RegistryAuth(s.config.Auth.Username, s.config.Auth.Password))
		}
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

func (s *Server) handleRegistryInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"name":"gordon-registry"}`)
}

func (s *Server) handleGetManifest(w http.ResponseWriter, r *http.Request) {
	name, reference := s.parseManifestPath(r.URL.Path)
	
	log.Debug().Str("name", name).Str("reference", reference).Msg("GET manifest")
	
	manifest, err := s.storage.GetManifest(name, reference)
	if err != nil {
		log.Warn().Err(err).Str("name", name).Str("reference", reference).Msg("Manifest not found")
		w.WriteHeader(http.StatusNotFound)
		return
	}
	
	w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	w.Header().Set("Content-Length", strconv.Itoa(len(manifest)))
	w.WriteHeader(http.StatusOK)
	w.Write(manifest)
}

func (s *Server) handlePutManifest(w http.ResponseWriter, r *http.Request) {
	name, reference := s.parseManifestPath(r.URL.Path)
	
	log.Debug().Str("name", name).Str("reference", reference).Msg("PUT manifest")
	
	// Read manifest data
	data, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error().Err(err).Msg("Failed to read manifest data")
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	
	// Store manifest
	if err := s.storage.PutManifest(name, reference, data); err != nil {
		log.Error().Err(err).Str("name", name).Str("reference", reference).Msg("Failed to store manifest")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	
	// Calculate digest for Docker-Content-Digest header
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", name, reference))
	w.WriteHeader(http.StatusCreated)
	
	// TODO: Trigger deployment webhook
	log.Info().Str("name", name).Str("reference", reference).Msg("Manifest stored - deployment should be triggered")
}

func (s *Server) handleGetBlob(w http.ResponseWriter, r *http.Request) {
	name, digest := s.parseBlobPath(r.URL.Path)
	
	log.Debug().Str("name", name).Str("digest", digest).Msg("GET blob")
	
	reader, err := s.storage.GetBlob(digest)
	if err != nil {
		log.Warn().Err(err).Str("digest", digest).Msg("Blob not found")
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer reader.Close()
	
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	
	_, err = io.Copy(w, reader)
	if err != nil {
		log.Error().Err(err).Str("digest", digest).Msg("Failed to serve blob")
	}
}

func (s *Server) handleStartBlobUpload(w http.ResponseWriter, r *http.Request) {
	name, _ := s.parseUploadPath(r.URL.Path)
	
	log.Debug().Str("name", name).Msg("Start blob upload")
	
	uuid, err := s.storage.StartBlobUpload(name)
	if err != nil {
		log.Error().Err(err).Str("name", name).Msg("Failed to start blob upload")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uuid))
	w.Header().Set("Range", "0-0")
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleBlobUpload(w http.ResponseWriter, r *http.Request) {
	name, uuid := s.parseUploadPath(r.URL.Path)
	
	log.Debug().Str("name", name).Str("uuid", uuid).Msg("Blob upload")
	
	// Check if this is a completion request (has digest parameter)
	digest := r.URL.Query().Get("digest")
	
	if r.Method == "PUT" && digest != "" {
		// This is a completion request
		if err := s.storage.FinishBlobUpload(uuid, digest); err != nil {
			log.Error().Err(err).Str("uuid", uuid).Str("digest", digest).Msg("Failed to finish blob upload")
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusCreated)
		
		log.Info().Str("name", name).Str("uuid", uuid).Str("digest", digest).Msg("Blob upload completed")
		return
	}
	
	// This is a chunk upload (PATCH)
	writer, err := s.storage.GetBlobUpload(uuid)
	if err != nil {
		log.Error().Err(err).Str("uuid", uuid).Msg("Failed to get blob upload")
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	defer writer.Close()
	
	written, err := io.Copy(writer, r.Body)
	if err != nil {
		log.Error().Err(err).Str("uuid", uuid).Msg("Failed to write blob chunk")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uuid))
	w.Header().Set("Range", fmt.Sprintf("0-%d", written-1))
	w.WriteHeader(http.StatusAccepted)
	
	log.Debug().Str("uuid", uuid).Int64("bytes", written).Msg("Blob chunk uploaded")
}

func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	name := s.parseTagsPath(r.URL.Path)
	
	log.Debug().Str("name", name).Msg("List tags")
	
	tags, err := s.storage.ListTags(name)
	if err != nil {
		log.Error().Err(err).Str("name", name).Msg("Failed to list tags")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	
	// Build JSON response
	fmt.Fprintf(w, `{"name":"%s","tags":[`, name)
	for i, tag := range tags {
		if i > 0 {
			fmt.Fprint(w, ",")
		}
		fmt.Fprintf(w, `"%s"`, tag)
	}
	fmt.Fprint(w, "]}")
}