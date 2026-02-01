// Package grpcregistry implements the gRPC server for the registry component.
// This server provides read-only manifest inspection operations.
package grpcregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/bnema/gordon/internal/boundaries/in"
	gordon "github.com/bnema/gordon/internal/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements the RegistryInspectService gRPC interface.
type Server struct {
	gordon.UnimplementedRegistryInspectServiceServer
	registrySvc in.RegistryService
}

// NewServer creates a new registry gRPC server.
func NewServer(registrySvc in.RegistryService) *Server {
	return &Server{
		registrySvc: registrySvc,
	}
}

// GetManifest retrieves a manifest by name and reference.
func (s *Server) GetManifest(ctx context.Context, req *gordon.GetManifestRequest) (*gordon.GetManifestResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "repository name is required")
	}
	if req.Reference == "" {
		return nil, status.Error(codes.InvalidArgument, "reference is required")
	}

	manifest, err := s.registrySvc.GetManifest(ctx, req.Name, req.Reference)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "manifest not found: %s:%s: %v", req.Name, req.Reference, err)
	}

	return &gordon.GetManifestResponse{
		Manifest: &gordon.Manifest{
			MediaType:   manifest.ContentType,
			Size:        int64(len(manifest.Data)),
			Digest:      calculateDigest(manifest.Data),
			Content:     manifest.Data,
			Annotations: manifest.Annotations,
		},
		Found: true,
	}, nil
}

// ListTags returns all tags for a repository.
func (s *Server) ListTags(ctx context.Context, req *gordon.ListTagsRequest) (*gordon.ListTagsResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "repository name is required")
	}

	tags, err := s.registrySvc.ListTags(ctx, req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "repository not found: %s: %v", req.Name, err)
	}

	return &gordon.ListTagsResponse{
		Name: req.Name,
		Tags: tags,
	}, nil
}

// ListRepositories returns all repository names.
func (s *Server) ListRepositories(ctx context.Context, _ *gordon.ListRepositoriesRequest) (*gordon.ListRepositoriesResponse, error) {
	repos, err := s.registrySvc.ListRepositories(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list repositories: %v", err)
	}

	return &gordon.ListRepositoriesResponse{
		Repositories: repos,
	}, nil
}

// calculateDigest computes the sha256 digest from manifest data.
func calculateDigest(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:])
}
