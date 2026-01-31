// Package grpccore implements the gRPC client for the core service.
// This client implements out.TargetResolver and out.RouteChangeWatcher for the proxy component.
package grpccore

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	gordonv1 "github.com/bnema/gordon/internal/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

// Client implements the out.TargetResolver and out.RouteChangeWatcher interfaces
// by making gRPC calls to the gordon-core service.
type Client struct {
	client       gordonv1.CoreServiceClient
	conn         *grpc.ClientConn
	coreAddr     string
	log          zerowrap.Logger
	onInvalidate func(domain string)
}

// NewClient creates a new gRPC client for the core service.
func NewClient(coreAddr string, log zerowrap.Logger) (*Client, error) {
	if coreAddr == "" {
		coreAddr = "gordon-core:9090" // Default internal network address
	}

	// Use grpc.NewClient (preferred in gRPC 1.x) with timeout via context
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(coreAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to core service at %s: %w", coreAddr, err)
	}

	// Wait for connection to be ready with timeout
	for {
		state := conn.GetState()
		if state == connectivity.Ready {
			break
		}
		if !conn.WaitForStateChange(ctx, state) {
			conn.Close()
			return nil, fmt.Errorf("timeout connecting to core service at %s", coreAddr)
		}
	}

	return &Client{
		client:   gordonv1.NewCoreServiceClient(conn),
		conn:     conn,
		coreAddr: coreAddr,
		log:      log,
	}, nil
}

// GetTarget resolves a domain to its proxy target via gRPC.
func (c *Client) GetTarget(ctx context.Context, domainName string) (*domain.ProxyTarget, error) {
	log := c.log.With().
		Str("domain", domainName).
		Str("usecase", "GetTarget").
		Logger()

	resp, err := c.client.GetTarget(ctx, &gordonv1.GetTargetRequest{
		Domain: domainName,
	})
	if err != nil {
		log.Warn().Err(err).Msg("failed to get target from core")
		return nil, fmt.Errorf("core service error: %w", err)
	}

	if !resp.Found || resp.Target == nil {
		return nil, domain.ErrNoTargetAvailable
	}

	target := &domain.ProxyTarget{
		Host:        resp.Target.Host,
		Port:        int(resp.Target.Port),
		ContainerID: resp.Target.ContainerId,
		Scheme:      resp.Target.Scheme,
	}

	log.Debug().
		Str("host", target.Host).
		Int("port", target.Port).
		Str("container_id", target.ContainerID).
		Msg("resolved target from core")

	return target, nil
}

// GetRoutes returns all configured routes via gRPC.
func (c *Client) GetRoutes(ctx context.Context) ([]domain.Route, error) {
	resp, err := c.client.GetRoutes(ctx, &gordonv1.GetRoutesRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to get routes from core: %w", err)
	}

	routes := make([]domain.Route, len(resp.Routes))
	for i, r := range resp.Routes {
		routes[i] = domain.Route{
			Domain: r.Domain,
			Image:  r.Image,
			HTTPS:  r.Https,
		}
	}

	return routes, nil
}

// GetExternalRoutes returns external route mappings via gRPC.
func (c *Client) GetExternalRoutes(ctx context.Context) (map[string]string, error) {
	resp, err := c.client.GetExternalRoutes(ctx, &gordonv1.GetExternalRoutesRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to get external routes from core: %w", err)
	}

	return resp.Routes, nil
}

// Watch starts watching for route changes via gRPC streaming.
// The onInvalidate callback is called when a route is invalidated.
// This method blocks until the context is cancelled.
func (c *Client) Watch(ctx context.Context, onInvalidate func(domainName string)) error {
	log := c.log.With().
		Str("usecase", "WatchRouteChanges").
		Logger()

	c.onInvalidate = onInvalidate

	// Wait for connection to be ready
	for c.conn.GetState() != connectivity.Ready {
		if !c.conn.WaitForStateChange(ctx, c.conn.GetState()) {
			return ctx.Err()
		}
	}

	stream, err := c.client.WatchRouteChanges(ctx, &gordonv1.WatchRouteChangesRequest{})
	if err != nil {
		return fmt.Errorf("failed to start route change watch: %w", err)
	}

	log.Info().Msg("connected to core route change stream")

	for {
		event, err := stream.Recv()
		if err == io.EOF {
			log.Info().Msg("route change stream closed by server")
			return nil
		}
		if err != nil {
			log.Warn().Err(err).Msg("route change stream error")
			return fmt.Errorf("route change stream error: %w", err)
		}

		switch event.Type {
		case gordonv1.RouteChangeEvent_INVALIDATE:
			log.Debug().Str("domain", event.Domain).Msg("route invalidated")
			if c.onInvalidate != nil {
				c.onInvalidate(event.Domain)
			}
		case gordonv1.RouteChangeEvent_INVALIDATE_ALL:
			log.Debug().Msg("all routes invalidated")
			if c.onInvalidate != nil {
				c.onInvalidate("") // Empty domain means invalidate all
			}
		}
	}
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// HealthCheck returns true if the connection is ready.
func (c *Client) HealthCheck() bool {
	return c.conn.GetState() == connectivity.Ready
}

// compile-time interface checks
var (
	_ out.TargetResolver     = (*Client)(nil)
	_ out.RouteChangeWatcher = (*Client)(nil)
)
