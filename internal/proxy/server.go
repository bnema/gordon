package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
	"gordon/internal/config"
	"gordon/internal/container"
	"gordon/internal/events"
	"gordon/internal/middleware"
)

type Server struct {
	config   *config.Config
	mux      *http.ServeMux
	routes   []config.Route
	manager  container.ManagerInterface
}

func NewServer(cfg *config.Config, manager container.ManagerInterface) *Server {
	s := &Server{
		config:  cfg,
		mux:     http.NewServeMux(),
		routes:  cfg.GetRoutes(),
		manager: manager,
	}
	s.setupRoutes()
	return s
}

// Handler returns the HTTP handler for testing
func (s *Server) Handler() http.Handler {
	return middleware.Chain(
		middleware.PanicRecovery,
		middleware.RequestLogger,
	)(s.mux)
}

func (s *Server) setupRoutes() {
	// Domain-based routing handler
	s.mux.HandleFunc("/", s.handleDomainRouting)
}

func (s *Server) handleDomainRouting(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	
	// Check if this is the registry domain
	if s.config.Server.RegistryDomain != "" && host == s.config.Server.RegistryDomain {
		log.Info().Str("domain", host).Msg("Routing request to registry")
		s.proxyToRegistry(w, r)
		return
	}
	
	// Find matching route for this domain
	for _, route := range s.routes {
		if route.Domain == host {
			log.Info().Str("domain", host).Str("image", route.Image).Msg("Routing request")
			s.proxyToContainer(w, r, route)
			return
		}
	}
	
	// No route found
	log.Warn().Str("domain", host).Msg("No route found for domain")
	http.NotFound(w, r)
}

func (s *Server) proxyToContainer(w http.ResponseWriter, r *http.Request, route config.Route) {
	ctx := r.Context()
	
	// Check if container exists and is running
	container, exists := s.manager.GetContainer(route.Domain)
	if !exists {
		log.Warn().Str("domain", route.Domain).Msg("No container found for route - container should be auto-deployed on image push")
		http.Error(w, "Service Unavailable - Container not deployed", http.StatusServiceUnavailable)
		return
	}

	// Detect if Gordon is running in a container or on the host
	var target *url.URL
	if s.isRunningInContainer() {
		// Gordon is in a container - use container network
		containerIP, containerPort, err := s.manager.Runtime().GetContainerNetworkInfo(ctx, container.ID)
		if err != nil {
			log.Error().Err(err).Str("domain", route.Domain).Str("container_id", container.ID).Msg("Failed to get container network info")
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}
		target, err = url.Parse(fmt.Sprintf("http://%s:%d", containerIP, containerPort))
		if err != nil {
			log.Error().Err(err).Msg("Failed to parse container target URL")
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		log.Debug().Str("mode", "container").Str("target", target.String()).Msg("Using container-to-container networking")
	} else {
		// Gordon is on the host - use host port mapping
		exposedPorts, err := s.manager.Runtime().GetImageExposedPorts(ctx, route.Image)
		if err != nil {
			log.Error().Err(err).Str("domain", route.Domain).Str("image", route.Image).Msg("Failed to get exposed ports from image")
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}
		
		hostPort, err := s.manager.Runtime().GetContainerPort(ctx, container.ID, exposedPorts[0])
		if err != nil {
			log.Error().Err(err).Str("domain", route.Domain).Str("container_id", container.ID).Int("internal_port", exposedPorts[0]).Msg("Failed to get host port mapping")
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}
		
		target, err = url.Parse(fmt.Sprintf("http://localhost:%d", hostPort))
		if err != nil {
			log.Error().Err(err).Msg("Failed to parse host target URL")
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		log.Debug().Str("mode", "host").Str("target", target.String()).Msg("Using host port mapping")
	}
	
	proxy := httputil.NewSingleHostReverseProxy(target)
	
	// Customize the proxy to handle errors
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Error().Err(err).Str("domain", route.Domain).Str("target", target.String()).Msg("Proxy error")
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
	}
	
	// Add custom headers
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("X-Proxied-By", "Gordon")
		resp.Header.Set("X-Container-ID", container.ID)
		return nil
	}
	
	log.Debug().
		Str("method", r.Method).
		Str("path", r.URL.Path).
		Str("domain", route.Domain).
		Str("target", target.String()).
		Str("container", container.ID).
		Msg("Proxying request")
	proxy.ServeHTTP(w, r)
}

func (s *Server) proxyToRegistry(w http.ResponseWriter, r *http.Request) {
	// Proxy to the internal registry server
	target, err := url.Parse(fmt.Sprintf("http://localhost:%d", s.config.Server.RegistryPort))
	if err != nil {
		log.Error().Err(err).Msg("Failed to parse registry target URL")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	
	proxy := httputil.NewSingleHostReverseProxy(target)
	
	// Customize the proxy to handle errors
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Error().Err(err).Int("registry_port", s.config.Server.RegistryPort).Msg("Registry proxy error")
		http.Error(w, "Registry Unavailable", http.StatusServiceUnavailable)
	}
	
	// Add custom headers
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("X-Proxied-By", "Gordon")
		resp.Header.Set("X-Registry-Backend", "gordon-registry")
		return nil
	}
	
	log.Debug().
		Str("method", r.Method).
		Str("path", r.URL.Path).
		Str("domain", s.config.Server.RegistryDomain).
		Str("target", target.String()).
		Msg("Proxying request to registry")
	
	proxy.ServeHTTP(w, r)
}

// isRunningInContainer detects if Gordon is running inside a container
func (s *Server) isRunningInContainer() bool {
	// Check for common container indicators
	
	// 1. Check for /.dockerenv file (Docker creates this)
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	
	// 2. Check cgroup for container indicators
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		content := string(data)
		if strings.Contains(content, "docker") || 
		   strings.Contains(content, "containerd") || 
		   strings.Contains(content, "podman") {
			return true
		}
	}
	
	// 3. Check if hostname matches container ID pattern (64 hex chars)
	if hostname, err := os.Hostname(); err == nil {
		if len(hostname) == 12 || len(hostname) == 64 {
			// Container hostnames are typically 12 or 64 character hex strings
			return true
		}
	}
	
	// 4. Check environment variables set by container runtimes
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" ||
	   os.Getenv("DOCKER_CONTAINER") != "" ||
	   os.Getenv("container") != "" {
		return true
	}
	
	return false
}

// UpdateConfig updates the config reference and refreshes routes
func (s *Server) UpdateConfig(newConfig *config.Config) {
	s.config = newConfig
	s.routes = s.config.GetRoutes()
	log.Debug().Int("route_count", len(s.routes)).Msg("Config and routes updated in proxy server")
}

// UpdateRoutes refreshes the routes from the current config
func (s *Server) UpdateRoutes() {
	s.routes = s.config.GetRoutes()
	log.Debug().Int("route_count", len(s.routes)).Msg("Routes updated in proxy server")
}

func (s *Server) Start(ctx context.Context) error {
	addr := ":" + strconv.Itoa(s.config.Server.Port)
	
	// Wrap the mux with middleware
	handler := middleware.Chain(
		middleware.PanicRecovery,
		middleware.RequestLogger,
		middleware.CORS,
	)(s.mux)
	
	// TODO: Implement HTTPS support with Let's Encrypt
	server := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	log.Info().Str("address", addr).Msg("Proxy server starting")

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
		log.Info().Msg("Proxy server shutting down...")
		return server.Shutdown(context.Background())
	}
}

// ProxyEventHandler handles events for the proxy server
type ProxyEventHandler struct {
	server *Server
}

func NewProxyEventHandler(server *Server) *ProxyEventHandler {
	return &ProxyEventHandler{
		server: server,
	}
}

func (h *ProxyEventHandler) CanHandle(eventType events.EventType) bool {
	return eventType == events.ConfigReload
}

func (h *ProxyEventHandler) Handle(event events.Event) error {
	if event.Type == events.ConfigReload {
		h.server.UpdateRoutes()
		return nil
	}
	return fmt.Errorf("unsupported event type: %s", event.Type)
}


