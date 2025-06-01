package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"

	"github.com/rs/zerolog/log"
	"gordon/internal/config"
	"gordon/internal/container"
	"gordon/internal/middleware"
)

type Server struct {
	config   *config.Config
	mux      *http.ServeMux
	routes   []config.Route
	manager  *container.Manager
}

func NewServer(cfg *config.Config, manager *container.Manager) *Server {
	s := &Server{
		config:  cfg,
		mux:     http.NewServeMux(),
		routes:  cfg.GetRoutes(),
		manager: manager,
	}
	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	// Protected Management API endpoints
	if s.config.Auth.Enabled {
		// Create protected subrouter for API endpoints
		apiMux := http.NewServeMux()
		apiMux.HandleFunc("GET /api/status", s.handleStatus)
		apiMux.HandleFunc("POST /api/reload", s.handleReload)
		apiMux.HandleFunc("GET /api/containers", s.handleListContainers)
		apiMux.HandleFunc("POST /api/deploy/{route}", s.handleDeploy)
		
		// Apply authentication middleware to API routes
		var authHandler http.Handler = apiMux
		
		switch s.config.Auth.Method {
		case "jwt":
			authHandler = middleware.JWTAuth(s.config.Auth.JWTSecret)(authHandler)
		case "api_key":
			authHandler = middleware.APIKeyAuth(s.config.Auth.APIKey)(authHandler)
		case "basic":
			authHandler = middleware.BasicAuth(s.config.Auth.Username, s.config.Auth.Password)(authHandler)
		}
		
		// Add IP whitelist if configured
		if len(s.config.Auth.AllowedIPs) > 0 {
			authHandler = middleware.IPWhitelist(s.config.Auth.AllowedIPs)(authHandler)
		}
		
		s.mux.Handle("/api/", authHandler)
	} else {
		// Unprotected API endpoints (not recommended for production)
		s.mux.HandleFunc("GET /api/status", s.handleStatus)
		s.mux.HandleFunc("POST /api/reload", s.handleReload)
		s.mux.HandleFunc("GET /api/containers", s.handleListContainers)
		s.mux.HandleFunc("POST /api/deploy/{route}", s.handleDeploy)
	}
	
	// Catch-all handler for domain-based routing
	// Since Go's ServeMux doesn't support Host matching, we'll handle it in the handler
	s.mux.HandleFunc("/", s.handleDomainRouting)
}

func (s *Server) handleDomainRouting(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	
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
		log.Info().Str("domain", route.Domain).Msg("Container not found, deploying")
		
		// Deploy the container
		var err error
		container, err = s.manager.DeployContainer(ctx, route)
		if err != nil {
			log.Error().Err(err).Str("domain", route.Domain).Msg("Failed to deploy container")
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}
	}

	// Try to get container port (try common web ports)
	var targetPort int
	var err error
	
	// Try common web ports in order: 80, 8080, 3000
	for _, port := range []int{80, 8080, 3000} {
		targetPort, err = s.manager.GetContainerPort(ctx, route.Domain, port)
		if err == nil {
			break
		}
	}
	
	if err != nil {
		log.Error().Err(err).Str("domain", route.Domain).Msg("No accessible port found for container")
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}
	
	target, err := url.Parse(fmt.Sprintf("http://localhost:%d", targetPort))
	if err != nil {
		log.Error().Err(err).Msg("Failed to parse target URL")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	
	proxy := httputil.NewSingleHostReverseProxy(target)
	
	// Customize the proxy to handle errors
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Error().Err(err).Str("domain", route.Domain).Int("port", targetPort).Msg("Proxy error")
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

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Content-Type", "application/json")
	
	// Get container health status
	health := s.manager.HealthCheck(ctx)
	containers := s.manager.ListContainers()
	
	status := map[string]interface{}{
		"status":     "running",
		"routes":     len(s.routes),
		"runtime":    s.config.Server.Runtime,
		"containers": len(containers),
		"health":     health,
	}
	
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"%s","routes":%d,"runtime":"%s","containers":%d}`, 
		status["status"], status["routes"], status["runtime"], status["containers"])
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	log.Info().Msg("Configuration reload requested")
	
	// TODO: Implement configuration reload
	// This should reload the config file and update routes
	
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"message":"Configuration reloaded"}`)
}

func (s *Server) handleListContainers(w http.ResponseWriter, r *http.Request) {
	log.Info().Msg("Container list requested")
	
	containers := s.manager.ListContainers()
	
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	
	// Simple JSON response - in production would use proper JSON marshaling
	fmt.Fprint(w, `{"containers":[`)
	first := true
	for domain, container := range containers {
		if !first {
			fmt.Fprint(w, ",")
		}
		fmt.Fprintf(w, `{"domain":"%s","id":"%s","image":"%s","status":"%s"}`, 
			domain, container.ID, container.Image, container.Status)
		first = false
	}
	fmt.Fprint(w, `]}`)
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	routeName := r.PathValue("route")
	if routeName == "" {
		http.Error(w, "Route name required", http.StatusBadRequest)
		return
	}
	
	log.Info().Str("route", routeName).Msg("Manual deployment requested")
	
	// Find the route
	var targetRoute *config.Route
	for _, route := range s.routes {
		if route.Domain == routeName {
			targetRoute = &route
			break
		}
	}
	
	if targetRoute == nil {
		http.Error(w, "Route not found", http.StatusNotFound)
		return
	}
	
	// Deploy the container
	ctx := r.Context()
	container, err := s.manager.DeployContainer(ctx, *targetRoute)
	if err != nil {
		log.Error().Err(err).Str("route", routeName).Msg("Deployment failed")
		http.Error(w, "Deployment failed", http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"message":"Deployed successfully","container":"%s","domain":"%s","image":"%s"}`, 
		container.ID, targetRoute.Domain, targetRoute.Image)
}