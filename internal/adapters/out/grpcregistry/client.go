// Package grpcregistry implements a gRPC client for the registry service.
// This client is used by gordon-core to inspect manifests and tags remotely.
package grpcregistry

import (
	"context"
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/domain"
	gordonv1 "github.com/bnema/gordon/internal/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

// Client provides remote access to registry operations via gRPC.
// It connects to the gordon-registry service for manifest inspection.
type Client struct {
	conn   *grpc.ClientConn
	client gordonv1.RegistryInspectServiceClient
}

// NewClient creates a new gRPC client for the registry service.
func NewClient(addr string) (*Client, error) {
	if addr == "" {
		addr = "gordon-registry:9092" // Default internal network address
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to registry service at %s: %w", addr, err)
	}

	// Wait for connection with timeout
	state := conn.GetState()
	if state != connectivity.Ready {
		if !conn.WaitForStateChange(ctx, state) {
			conn.Close()
			return nil, fmt.Errorf("timeout connecting to registry service at %s", addr)
		}
	}

	return &Client{
		conn:   conn,
		client: gordonv1.NewRegistryInspectServiceClient(conn),
	}, nil
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// GetManifest retrieves a manifest by name and reference (tag or digest).
func (c *Client) GetManifest(ctx context.Context, name, reference string) (*domain.Manifest, error) {
	resp, err := c.client.GetManifest(ctx, &gordonv1.GetManifestRequest{
		Name:      name,
		Reference: reference,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get manifest: %w", err)
	}

	if resp.Manifest == nil {
		return nil, fmt.Errorf("manifest not found: %s@%s", name, reference)
	}

	return &domain.Manifest{
		Name:        name,
		Reference:   reference,
		ContentType: resp.Manifest.MediaType,
		Data:        resp.Manifest.Content,
		Digest:      resp.Manifest.Digest,
		Size:        resp.Manifest.Size,
		Annotations: resp.Manifest.Annotations,
	}, nil
}

// ListTags returns all tags for a repository.
func (c *Client) ListTags(ctx context.Context, name string) ([]string, error) {
	resp, err := c.client.ListTags(ctx, &gordonv1.ListTagsRequest{
		Name: name,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list tags: %w", err)
	}

	return resp.Tags, nil
}

// ListRepositories returns all repository names in the registry.
func (c *Client) ListRepositories(ctx context.Context) ([]string, error) {
	resp, err := c.client.ListRepositories(ctx, &gordonv1.ListRepositoriesRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list repositories: %w", err)
	}

	return resp.Repositories, nil
}

// IsHealthy returns true if the gRPC connection is ready.
func (c *Client) IsHealthy() bool {
	return c.conn.GetState() == connectivity.Ready
}
