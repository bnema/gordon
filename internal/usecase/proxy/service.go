// Package proxy implements the reverse proxy use case.
package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/bnema/zerowrap"

	"gordon/internal/boundaries/in"
	"gordon/internal/boundaries/out"
	"gordon/internal/domain"
)

// Config holds configuration needed by the proxy service.
type Config struct {
	RegistryDomain string
	RegistryPort   int
}

// Service implements the ProxyService interface.
type Service struct {
	runtime      out.ContainerRuntime
	containerSvc in.ContainerService
	configSvc    in.ConfigService
	config       Config
	targets      map[string]*domain.ProxyTarget
	mu           sync.RWMutex
}

// NewService creates a new proxy service.
func NewService(
	runtime out.ContainerRuntime,
	containerSvc in.ContainerService,
	configSvc in.ConfigService,
	config Config,
) *Service {
	return &Service{
		runtime:      runtime,
		containerSvc: containerSvc,
		configSvc:    configSvc,
		config:       config,
		targets:      make(map[string]*domain.ProxyTarget),
	}
}

// ServeHTTP handles incoming HTTP requests and proxies them to the appropriate backend.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Enrich request context with fields for downstream logging
	ctx := zerowrap.CtxWithFields(r.Context(), map[string]any{
		zerowrap.FieldLayer:    "adapter",
		zerowrap.FieldAdapter:  "http",
		zerowrap.FieldMethod:   r.Method,
		zerowrap.FieldPath:     r.URL.Path,
		zerowrap.FieldHost:     r.Host,
		zerowrap.FieldClientIP: r.RemoteAddr,
	})
	r = r.WithContext(ctx)
	log := zerowrap.FromCtx(ctx)

	// Check if this is the registry domain
	if s.config.RegistryDomain != "" && r.Host == s.config.RegistryDomain {
		log.Info().Msg("routing request to registry")
		s.proxyToRegistry(w, r)
		return
	}

	// Get target for this domain
	target, err := s.GetTarget(ctx, r.Host)
	if err != nil {
		log.Warn().Err(err).Msg("no route found for domain")
		http.NotFound(w, r)
		return
	}

	s.proxyToTarget(w, r, target)
}

// GetTarget returns the proxy target for a given domain.
func (s *Service) GetTarget(ctx context.Context, domainName string) (*domain.ProxyTarget, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "GetTarget",
		"domain":              domainName,
	})
	log := zerowrap.FromCtx(ctx)

	// Check cache first
	s.mu.RLock()
	if target, exists := s.targets[domainName]; exists {
		s.mu.RUnlock()
		return target, nil
	}
	s.mu.RUnlock()

	// Get container for this domain
	container, exists := s.containerSvc.Get(ctx, domainName)
	if !exists {
		return nil, domain.ErrNoTargetAvailable
	}

	// Build target based on runtime mode
	var target *domain.ProxyTarget

	if s.isRunningInContainer() {
		// Gordon is in a container - use container network
		containerIP, containerPort, err := s.runtime.GetContainerNetworkInfo(ctx, container.ID)
		if err != nil {
			return nil, log.WrapErrWithFields(err, "failed to get container network info", map[string]any{zerowrap.FieldEntityID: container.ID})
		}
		target = &domain.ProxyTarget{
			Host:        containerIP,
			Port:        containerPort,
			ContainerID: container.ID,
			Scheme:      "http",
		}
	} else {
		// Gordon is on the host - use host port mapping
		routes := s.configSvc.GetRoutes(ctx)
		var route *domain.Route
		for _, r := range routes {
			if r.Domain == domainName {
				route = &r
				break
			}
		}

		if route == nil {
			return nil, domain.ErrRouteNotFound
		}

		exposedPorts, err := s.runtime.GetImageExposedPorts(ctx, route.Image)
		if err != nil || len(exposedPorts) == 0 {
			return nil, log.WrapErr(err, "failed to get exposed ports")
		}

		hostPort, err := s.runtime.GetContainerPort(ctx, container.ID, exposedPorts[0])
		if err != nil {
			return nil, log.WrapErrWithFields(err, "failed to get host port mapping", map[string]any{"internal_port": exposedPorts[0]})
		}

		target = &domain.ProxyTarget{
			Host:        "localhost",
			Port:        hostPort,
			ContainerID: container.ID,
			Scheme:      "http",
		}
	}

	// Cache the target
	s.mu.Lock()
	s.targets[domainName] = target
	s.mu.Unlock()

	return target, nil
}

// RegisterTarget registers a new proxy target for a domain.
func (s *Service) RegisterTarget(_ context.Context, domainName string, target *domain.ProxyTarget) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.targets[domainName] = target
	return nil
}

// UnregisterTarget removes a proxy target for a domain.
func (s *Service) UnregisterTarget(_ context.Context, domainName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.targets, domainName)
	return nil
}

// RefreshTargets refreshes all proxy targets from container state.
func (s *Service) RefreshTargets(ctx context.Context) error {
	s.mu.Lock()
	s.targets = make(map[string]*domain.ProxyTarget)
	s.mu.Unlock()

	log := zerowrap.FromCtx(ctx)
	log.Debug().Msg("proxy targets cache cleared")
	return nil
}

// UpdateConfig updates the service configuration.
func (s *Service) UpdateConfig(config Config) {
	s.config = config
}

// Helper methods

func (s *Service) proxyToTarget(w http.ResponseWriter, r *http.Request, target *domain.ProxyTarget) {
	log := zerowrap.FromCtx(r.Context())

	targetURL, err := url.Parse(fmt.Sprintf("%s://%s:%d", target.Scheme, target.Host, target.Port))
	if err != nil {
		log.WrapErr(err, "failed to parse target URL")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Error().Err(err).Str("target", targetURL.String()).Msg("proxy error")
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("X-Proxied-By", "Gordon")
		resp.Header.Set("X-Container-ID", target.ContainerID)
		return nil
	}

	log.Debug().
		Str("target", targetURL.String()).
		Str(zerowrap.FieldEntityID, target.ContainerID).
		Msg("proxying request")

	proxy.ServeHTTP(w, r)
}

func (s *Service) proxyToRegistry(w http.ResponseWriter, r *http.Request) {
	log := zerowrap.FromCtx(r.Context())

	targetURL, err := url.Parse(fmt.Sprintf("http://localhost:%d", s.config.RegistryPort))
	if err != nil {
		log.WrapErr(err, "failed to parse registry target URL")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Error().Err(err).Int("registry_port", s.config.RegistryPort).Msg("registry proxy error")
		http.Error(w, "Registry Unavailable", http.StatusServiceUnavailable)
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("X-Proxied-By", "Gordon")
		resp.Header.Set("X-Registry-Backend", "gordon-registry")
		return nil
	}

	log.Debug().Str("target", targetURL.String()).Msg("proxying request to registry")

	proxy.ServeHTTP(w, r)
}

func (s *Service) isRunningInContainer() bool {
	// Check for /.dockerenv
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}

	// Check cgroup for container indicators
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		content := string(data)
		if strings.Contains(content, "docker") ||
			strings.Contains(content, "containerd") ||
			strings.Contains(content, "podman") {
			return true
		}
	}

	// Check hostname pattern
	if hostname, err := os.Hostname(); err == nil {
		if len(hostname) == 12 || len(hostname) == 64 {
			return true
		}
	}

	// Check environment variables
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" ||
		os.Getenv("DOCKER_CONTAINER") != "" ||
		os.Getenv("container") != "" {
		return true
	}

	return false
}
