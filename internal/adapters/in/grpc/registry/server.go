// Package grpcregistry implements the gRPC server for the registry component.
// This server provides read-only manifest inspection operations.
package grpcregistry

import (
	"context"

	"github.com/bnema/gordon/internal/boundaries/in"
	gordonv1 "github.com/bnema/gordon/internal/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements the RegistryInspectService gRPC interface.
type Server struct {
	gordonv1.UnimplementedRegistryInspectServiceServer
	registrySvc in.RegistryService
}

// NewServer creates a new registry gRPC server.
func NewServer(registrySvc in.RegistryService) *Server {
	return &Server{
		registrySvc: registrySvc,
	}
}

// GetManifest retrieves a manifest by name and reference.
func (s *Server) GetManifest(ctx context.Context, req *gordonv1.GetManifestRequest) (*gordonv1.GetManifestResponse, error) {
	manifest, err := s.registrySvc.GetManifest(ctx, req.Name, req.Reference)
	if err != nil {
		return &gordonv1.GetManifestResponse{Found: false}, nil
	}

	return &gordonv1.GetManifestResponse{
		Manifest: &gordonv1.Manifest{
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
func (s *Server) ListTags(ctx context.Context, req *gordonv1.ListTagsRequest) (*gordonv1.ListTagsResponse, error) {
	tags, err := s.registrySvc.ListTags(ctx, req.Name)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list tags: %v", err)
	}

	return &gordonv1.ListTagsResponse{
		Name: req.Name,
		Tags: tags,
	}, nil
}

// ListRepositories returns all repository names.
func (s *Server) ListRepositories(ctx context.Context, _ *gordonv1.ListRepositoriesRequest) (*gordonv1.ListRepositoriesResponse, error) {
	repos, err := s.registrySvc.ListRepositories(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list repositories: %v", err)
	}

	return &gordonv1.ListRepositoriesResponse{
		Repositories: repos,
	}, nil
}

// calculateDigest computes a simple digest string from manifest data.
// In production, this should match Docker's sha256:... format.
func calculateDigest(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	// This is a placeholder - the actual digest calculation
	// is done by the registry service when storing manifests
	return ""
}
