// Package proxy implements the reverse proxy use case.
package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// proxyTransport is a shared HTTP transport with proper timeouts.
// This prevents resource exhaustion from slow backends or network issues.
var proxyTransport = &http.Transport{
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
	MaxIdleConns:          100,
	MaxIdleConnsPerHost:   10,
	IdleConnTimeout:       90 * time.Second,
}

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

// maxProxyBodySize is the maximum request body size for proxied requests (512MB).
// This prevents resource exhaustion from extremely large uploads through the proxy.
const maxProxyBodySize = 512 << 20

// ServeHTTP handles incoming HTTP requests and proxies them to the appropriate backend.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// SECURITY: Limit request body size to prevent resource exhaustion.
	r.Body = http.MaxBytesReader(w, r.Body, maxProxyBodySize)

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
	log.Debug().Str("resolving_target_for", r.Host).Msg("looking up proxy target")
	target, err := s.GetTarget(ctx, r.Host)
	if err != nil {
		log.Warn().Err(err).Msg("no route found for domain")
		http.NotFound(w, r)
		return
	}

	log.Debug().
		Str("host", target.Host).
		Int("port", target.Port).
		Str("container_id", target.ContainerID).
		Msg("resolved proxy target")

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
		log.Debug().
			Str("host", target.Host).
			Int("port", target.Port).
			Str("container_id", target.ContainerID).
			Msg("using cached proxy target")
		return target, nil
	}
	s.mu.RUnlock()

	// Check if this is an external route
	externalRoutes := s.configSvc.GetExternalRoutes()
	if targetAddr, ok := externalRoutes[domainName]; ok {
		host, portStr, err := net.SplitHostPort(targetAddr)
		if err != nil {
			return nil, log.WrapErrWithFields(err, "invalid external route target", map[string]any{"target": targetAddr})
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, log.WrapErrWithFields(err, "invalid port in external route", map[string]any{"target": targetAddr})
		}

		// SECURITY: Validate target is not an internal/blocked network (SSRF protection)
		if err := ValidateExternalRouteTarget(host); err != nil {
			log.Warn().
				Err(err).
				Str("host", host).
				Str("domain", domainName).
				Msg("SSRF protection: blocked external route to internal network")
			return nil, err
		}

		target := &domain.ProxyTarget{
			Host:        host,
			Port:        port,
			ContainerID: "", // Not a container
			Scheme:      "http",
		}

		// Cache external route target
		s.mu.Lock()
		s.targets[domainName] = target
		s.mu.Unlock()

		log.Debug().
			Str("host", host).
			Int("port", port).
			Msg("using external route target")
		return target, nil
	}

	// Get container for this domain
	container, exists := s.containerSvc.Get(ctx, domainName)
	if !exists {
		log.Debug().Msg("container not found for domain")
		return nil, domain.ErrNoTargetAvailable
	}
	log.Debug().Str("container_id", container.ID).Str("image", container.Image).Msg("found container for domain")

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

		// Determine target port: check for label first, then fall back to first exposed port
		// Use container.Image (the actual running image) instead of route.Image (config shorthand)
		targetPort, err := s.getProxyPort(ctx, container.Image)
		if err != nil {
			return nil, log.WrapErr(err, "failed to determine proxy port")
		}

		hostPort, err := s.runtime.GetContainerPort(ctx, container.ID, targetPort)
		if err != nil {
			return nil, log.WrapErrWithFields(err, "failed to get host port mapping", map[string]any{"internal_port": targetPort})
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

// InvalidateTarget removes a cached proxy target, forcing re-lookup on next request.
// This is used during zero-downtime deployments to switch traffic to a new container.
func (s *Service) InvalidateTarget(_ context.Context, domainName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.targets, domainName)
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

// newReverseProxy creates a reverse proxy using Rewrite instead of Director to prevent
// hop-by-hop header attacks. A malicious client could send "Connection: Authorization"
// to strip the Authorization header when using the default Director. Rewrite processes
// headers after hop-by-hop removal, ensuring headers like Authorization are preserved.
func newReverseProxy(targetURL *url.URL, errorHandler func(http.ResponseWriter, *http.Request, error), modifyResponse func(*http.Response) error) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(targetURL)
			// SECURITY: Let SetXForwarded() set X-Forwarded-Proto based on the actual
			// connection state (r.TLS). We do NOT preserve the original X-Forwarded-Proto
			// from the incoming request because we cannot verify it came from a trusted
			// proxy. An attacker could spoof X-Forwarded-Proto: https on an HTTP connection
			// to trick backends into thinking the connection is secure.
			pr.SetXForwarded()
			pr.Out.Host = targetURL.Host
		},
		Transport:      proxyTransport,
		ErrorHandler:   errorHandler,
		ModifyResponse: modifyResponse,
	}
}

func (s *Service) proxyToTarget(w http.ResponseWriter, r *http.Request, target *domain.ProxyTarget) {
	log := zerowrap.FromCtx(r.Context())

	targetURL, err := url.Parse(fmt.Sprintf("%s://%s:%d", target.Scheme, target.Host, target.Port))
	if err != nil {
		log.WrapErr(err, "failed to parse target URL")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	log.Debug().Str("target_url", targetURL.String()).Msg("proxying to target")

	proxy := newReverseProxy(targetURL,
		func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Error().Err(err).Str("target", targetURL.String()).Msg("proxy error: connection failed")
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		},
		func(resp *http.Response) error {
			resp.Header.Set("X-Proxied-By", "Gordon")
			return nil
		},
	)

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

	proxy := newReverseProxy(targetURL,
		func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Error().Err(err).Int("registry_port", s.config.RegistryPort).Msg("registry proxy error")
			http.Error(w, "Registry Unavailable", http.StatusServiceUnavailable)
		},
		func(resp *http.Response) error {
			resp.Header.Set("X-Proxied-By", "Gordon")
			resp.Header.Set("X-Registry-Backend", "gordon-registry")

			// Remove security headers from registry response to prevent duplicates.
			// The proxy middleware already adds these headers, so the registry's
			// copies would create duplicates in the final response.
			resp.Header.Del("X-Content-Type-Options")
			resp.Header.Del("X-Frame-Options")
			resp.Header.Del("X-XSS-Protection")
			resp.Header.Del("Referrer-Policy")
			resp.Header.Del("Permissions-Policy")

			return nil
		},
	)

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

	// NOTE: Hostname length check (12 or 64 chars) was removed because it produced
	// false positives on hosts with short hostnames (e.g., "web-server-1" = 12 chars),
	// which would cause the proxy to use container network IPs instead of host port mappings.

	// Check environment variables
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" ||
		os.Getenv("DOCKER_CONTAINER") != "" ||
		os.Getenv("container") != "" {
		return true
	}

	return false
}

// getProxyPort determines the port to proxy to for an image.
// It checks for the gordon.proxy.port label first, then falls back to the first exposed port.
func (s *Service) getProxyPort(ctx context.Context, imageRef string) (int, error) {
	log := zerowrap.FromCtx(ctx)

	log.Debug().Str("image_ref", imageRef).Msg("determining proxy port for image")

	// Check for explicit port label
	labels, err := s.runtime.GetImageLabels(ctx, imageRef)
	if err != nil {
		log.Debug().Err(err).Msg("failed to get image labels, falling back to exposed ports")
	} else if portStr, ok := labels[domain.LabelProxyPort]; ok {
		port, err := strconv.Atoi(portStr)
		if err == nil && port > 0 {
			log.Debug().Int("port", port).Msg("using proxy port from image label")
			return port, nil
		}
		log.Warn().Str("port_value", portStr).Msg("invalid gordon.proxy.port label value")
	}

	// Fall back to first exposed port
	exposedPorts, err := s.runtime.GetImageExposedPorts(ctx, imageRef)
	if err != nil {
		return 0, err
	}
	if len(exposedPorts) == 0 {
		return 0, fmt.Errorf("no exposed ports found for image %s", imageRef)
	}

	log.Debug().Int("port", exposedPorts[0]).Msg("using first exposed port")
	return exposedPorts[0], nil
}
