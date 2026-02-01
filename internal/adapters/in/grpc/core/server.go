// Package grpccore implements the gRPC server for the core component.
// This server exposes target resolution, route management, and event streaming.
package grpccore

import (
	"bytes"
	"context"
	"net"
	"os"
	"strconv"
	"sync"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	gordon "github.com/bnema/gordon/internal/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements the CoreService gRPC interface.
type Server struct {
	gordon.UnimplementedCoreServiceServer
	containerSvc in.ContainerService
	configSvc    in.ConfigService
	runtime      out.ContainerRuntime
	eventBus     out.EventBus
	log          zerowrap.Logger

	// Route change streaming
	watchersMu sync.RWMutex
	watchers   map[string]chan *gordon.RouteChangeEvent
	watcherID  int
}

// NewServer creates a new core gRPC server.
func NewServer(
	containerSvc in.ContainerService,
	configSvc in.ConfigService,
	runtime out.ContainerRuntime,
	eventBus out.EventBus,
	log zerowrap.Logger,
) *Server {
	s := &Server{
		containerSvc: containerSvc,
		configSvc:    configSvc,
		runtime:      runtime,
		eventBus:     eventBus,
		log:          log,
		watchers:     make(map[string]chan *gordon.RouteChangeEvent),
	}

	// Subscribe to events to stream route changes
	if eventBus != nil {
		if err := eventBus.Subscribe(&routeChangeHandler{server: s}); err != nil {
			log.Warn().Err(err).Msg("failed to subscribe to event bus")
		}
	}

	return s
}

// GetTarget resolves a domain to its proxy target.
func (s *Server) GetTarget(ctx context.Context, req *gordon.GetTargetRequest) (*gordon.GetTargetResponse, error) {
	log := s.log.With().
		Str("domain", req.Domain).
		Str("usecase", "GetTarget").
		Logger()
	ctx = log.WithContext(ctx)

	// Check if this is an external route first
	externalRoutes := s.configSvc.GetExternalRoutes()
	if targetAddr, ok := externalRoutes[req.Domain]; ok {
		host, portStr, err := net.SplitHostPort(targetAddr)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "invalid external route target: %v", err)
		}
		// Parse port as int64 first, then validate before converting to int32
		port64, err := strconv.ParseInt(portStr, 10, 64)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "invalid port in external route: %v", err)
		}
		if port64 < 1 || port64 > 65535 {
			return nil, status.Errorf(codes.Internal, "port out of valid range: %d", port64)
		}
		port := int32(port64)

		return &gordon.GetTargetResponse{
			Target: &gordon.Target{
				Host:        host,
				Port:        port,
				ContainerId: "", // Not a container
				Scheme:      "http",
			},
			Found: true,
		}, nil
	}

	// Get container for this domain
	container, exists := s.containerSvc.Get(ctx, req.Domain)
	if !exists {
		log.Debug().Msg("container not found for domain")
		return &gordon.GetTargetResponse{Found: false}, nil
	}

	log.Debug().
		Str("container_id", container.ID).
		Str("image", container.Image).
		Msg("found container for domain")

	// Build target based on runtime mode (container vs host)
	var target *gordon.Target

	if s.isRunningInContainer() {
		// Gordon is in a container - use container network
		containerIP, containerPort, err := s.runtime.GetContainerNetworkInfo(ctx, container.ID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get container network info: %v", err)
		}
		target = &gordon.Target{
			Host:        containerIP,
			Port:        int32(containerPort), // #nosec G115 - Container ports are always within valid range
			ContainerId: container.ID,
			Scheme:      "http",
		}
	} else {
		// Gordon is on the host - use host port mapping
		routes := s.configSvc.GetRoutes(ctx)
		var route *domain.Route
		for _, r := range routes {
			if r.Domain == req.Domain {
				route = &r
				break
			}
		}

		if route == nil {
			return &gordon.GetTargetResponse{Found: false}, nil
		}

		// Get the exposed port from container config
		targetPort := s.getProxyPort(container.Image)
		hostPort, err := s.runtime.GetContainerPort(ctx, container.ID, targetPort)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get host port mapping: %v", err)
		}

		target = &gordon.Target{
			Host:        "localhost",
			Port:        int32(hostPort), // #nosec G115 - Host ports are always within valid range
			ContainerId: container.ID,
			Scheme:      "http",
		}
	}

	return &gordon.GetTargetResponse{
		Target: target,
		Found:  true,
	}, nil
}

// GetRoutes returns all configured routes.
func (s *Server) GetRoutes(ctx context.Context, _ *gordon.GetRoutesRequest) (*gordon.GetRoutesResponse, error) {
	routes := s.configSvc.GetRoutes(ctx)
	protoRoutes := make([]*gordon.Route, len(routes))

	for i, r := range routes {
		protoRoutes[i] = &gordon.Route{
			Domain:   r.Domain,
			Image:    r.Image,
			Https:    r.HTTPS,
			External: false,
		}
	}

	return &gordon.GetRoutesResponse{Routes: protoRoutes}, nil
}

// GetExternalRoutes returns all external route mappings.
func (s *Server) GetExternalRoutes(ctx context.Context, _ *gordon.GetExternalRoutesRequest) (*gordon.GetExternalRoutesResponse, error) {
	externalRoutes := s.configSvc.GetExternalRoutes()
	return &gordon.GetExternalRoutesResponse{
		Routes: externalRoutes,
	}, nil
}

// NotifyImagePushed handles image push notifications from the registry.
func (s *Server) NotifyImagePushed(ctx context.Context, req *gordon.NotifyImagePushedRequest) (*gordon.NotifyImagePushedResponse, error) {
	log := s.log.With().
		Str("usecase", "NotifyImagePushed").
		Str("image", req.Name).
		Str("reference", req.Reference).
		Logger()
	ctx = log.WithContext(ctx)

	log.Info().Msg("image pushed notification received")

	// Find routes matching this image and trigger redeployment
	routes := s.configSvc.GetRoutes(ctx)
	var matchingRoute *domain.Route

	for _, r := range routes {
		if r.Image == req.Name {
			matchingRoute = &r
			break
		}
	}

	if matchingRoute != nil {
		log.Info().
			Str("domain", matchingRoute.Domain).
			Msg("triggering deployment for matching route")

		// Trigger deployment via event bus
		if s.eventBus != nil {
			payload := domain.ImagePushedPayload{
				Name:      req.Name,
				Reference: req.Reference,
				Manifest:  req.Manifest,
			}
			if err := s.eventBus.Publish(domain.EventImagePushed, payload); err != nil {
				log.Warn().Err(err).Msg("failed to publish image pushed event")
			}
		}
	}

	return &gordon.NotifyImagePushedResponse{Accepted: true}, nil
}

// WatchRouteChanges streams route change events to connected clients.
func (s *Server) WatchRouteChanges(_ *gordon.WatchRouteChangesRequest, stream gordon.CoreService_WatchRouteChangesServer) error {
	ctx := stream.Context()
	log := s.log.With().
		Str("usecase", "WatchRouteChanges").
		Logger()
	_ = log.WithContext(ctx)

	log.Info().Msg("new route change watcher connected")

	// Create a channel for this watcher
	s.watchersMu.Lock()
	s.watcherID++
	watcherID := strconv.Itoa(s.watcherID)
	eventCh := make(chan *gordon.RouteChangeEvent, 10)
	s.watchers[watcherID] = eventCh
	s.watchersMu.Unlock()

	// Use sync.Once to ensure channel is closed only once
	var closeOnce sync.Once

	// Cleanup on exit
	defer func() {
		s.watchersMu.Lock()
		delete(s.watchers, watcherID)
		s.watchersMu.Unlock()

		// Close the channel after removing from map to prevent sends to it
		closeOnce.Do(func() {
			close(eventCh)
		})

		log.Info().Msg("route change watcher disconnected")
	}()

	// Send events to the stream
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-eventCh:
			if !ok {
				return nil
			}
			if err := stream.Send(&gordon.WatchRouteChangesResponse{Event: event}); err != nil {
				log.Warn().Err(err).Msg("failed to send route change event")
				return err
			}
		}
	}
}

// BroadcastRouteChange broadcasts a route change event to all connected watchers.
func (s *Server) BroadcastRouteChange(changeType gordon.RouteChangeEvent_ChangeType, domain string) {
	s.watchersMu.RLock()
	defer s.watchersMu.RUnlock()

	event := &gordon.RouteChangeEvent{
		Type:   changeType,
		Domain: domain,
	}

	for id, ch := range s.watchers {
		select {
		case ch <- event:
			// Event sent successfully
		default:
			// Channel full or closed, log and continue
			s.log.Warn().
				Str("watcher_id", id).
				Msg("failed to broadcast route change, channel blocked")
		}
	}
}

// isRunningInContainer checks if Gordon is running inside a Docker container.
func (s *Server) isRunningInContainer() bool {
	// Check for container indicators
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	// Additional check: read /proc/1/cgroup
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		if bytes.Contains(data, []byte("docker")) || bytes.Contains(data, []byte("containerd")) {
			return true
		}
	}
	return false
}

// getProxyPort determines the target port for proxying based on the image.
func (s *Server) getProxyPort(image string) int {
	// Default to port 8080 if not specified
	// In a real implementation, this might check container labels or config
	_ = image
	return 8080
}

// routeChangeHandler implements out.EventHandler to receive domain events.
type routeChangeHandler struct {
	server *Server
}

func (h *routeChangeHandler) Handle(event domain.Event) error {
	switch event.Type {
	case domain.EventImagePushed:
		// When an image is pushed, check if it's for a specific route
		if event.Route != "" {
			h.server.BroadcastRouteChange(gordon.RouteChangeEvent_CHANGE_TYPE_INVALIDATE, event.Route)
		}
	case domain.EventConfigReload:
		// Config reload invalidates all routes
		h.server.BroadcastRouteChange(gordon.RouteChangeEvent_CHANGE_TYPE_INVALIDATE_ALL, "")
	}
	return nil
}

func (h *routeChangeHandler) CanHandle(eventType domain.EventType) bool {
	switch eventType {
	case domain.EventImagePushed, domain.EventConfigReload:
		return true
	default:
		return false
	}
}
