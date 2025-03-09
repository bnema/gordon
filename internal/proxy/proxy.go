package proxy

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/interfaces"
	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// Proxy represents the reverse proxy server
type Proxy struct {
	config        common.ReverseProxyConfig
	app           interfaces.AppInterface
	httpsServer   *echo.Echo
	httpServer    *echo.Echo
	routes        map[string]*ProxyRouteInfo
	mu            sync.RWMutex
	certManager   *autocert.Manager
	serverStarted bool
	blacklist     *BlacklistConfig

	// Fields for throttling blacklist logs
	lastBlockedLog   time.Time
	blockedIPCounter map[string]int
	blockedIPCountMu sync.Mutex
}

// ProxyRouteInfo contains the information needed to route traffic to a container
type ProxyRouteInfo struct {
	Domain        string
	ContainerIP   string
	ContainerPort string
	ContainerID   string
	Protocol      string
	Path          string
	Active        bool
}

// Create blacklistedIPs set to track IPs for middleware skipping
type requestContext struct {
	blacklisted bool
}

// NewProxy creates a new reverse proxy
func NewProxy(app interfaces.AppInterface) (*Proxy, error) {
	config := app.GetConfig().ReverseProxy

	// Create the routes map
	routes := make(map[string]*ProxyRouteInfo)

	// Initialize the blacklist
	// Try both .yml and .yaml extensions, preferring .yml
	var blacklist *BlacklistConfig
	var err error

	storageDir := app.GetConfig().General.StorageDir
	// Prefer .yml over .yaml for consistency with config.yml
	blacklistPath := storageDir + "/blacklist.yml"
	blacklistLegacy := storageDir + "/blacklist.yaml"

	// Check if legacy blacklist.yaml exists but not .yml
	if _, statErrLegacy := os.Stat(blacklistLegacy); statErrLegacy == nil {
		if _, statErrYml := os.Stat(blacklistPath); statErrYml != nil && os.IsNotExist(statErrYml) {
			log.Info("Found legacy blacklist.yaml file, using it", "path", blacklistLegacy)
			blacklist, err = NewBlacklist(blacklistLegacy)
		} else {
			// Both exist or only .yml exists, prefer .yml
			log.Debug("Using blacklist.yml file", "path", blacklistPath)
			blacklist, err = NewBlacklist(blacklistPath)
		}
	} else {
		// No legacy file, use .yml
		log.Debug("Using blacklist.yml file", "path", blacklistPath)
		blacklist, err = NewBlacklist(blacklistPath)
	}

	if err != nil {
		log.Error("Failed to initialize blacklist", "error", err)
		// Continue anyway with nil blacklist
	}

	// Create the HTTPS echo instance
	httpsServer := echo.New()
	httpsServer.HideBanner = true
	httpsServer.Use(middleware.Recover())

	// Custom logger that skips blocked IPs
	httpsServer.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Store the context in the request context
			c.Set("reqContext", &requestContext{blacklisted: false})
			return next(c)
		}
	})

	httpsServer.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
		Skipper: func(c echo.Context) bool {
			// Skip logging if the request is marked as blacklisted
			if reqCtx, ok := c.Get("reqContext").(*requestContext); ok && reqCtx.blacklisted {
				return true
			}
			return false
		},
	}))

	// Create the HTTP echo instance (for redirects to HTTPS)
	httpServer := echo.New()
	httpServer.HideBanner = true
	httpServer.Use(middleware.Recover())

	// Custom logger that skips blocked IPs
	httpServer.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Store the context in the request context
			c.Set("reqContext", &requestContext{blacklisted: false})
			return next(c)
		}
	})

	httpServer.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
		Skipper: func(c echo.Context) bool {
			// Skip logging if the request is marked as blacklisted
			if reqCtx, ok := c.Get("reqContext").(*requestContext); ok && reqCtx.blacklisted {
				return true
			}
			return false
		},
	}))

	// Create the proxy
	p := &Proxy{
		config:           config,
		app:              app,
		httpsServer:      httpsServer,
		httpServer:       httpServer,
		routes:           routes,
		serverStarted:    false,
		blacklist:        blacklist,
		blockedIPCounter: make(map[string]int),
		lastBlockedLog:   time.Time{}, // Zero time
	}

	// Set up the certificate manager
	p.setupCertManager()

	log.Debug("Reverse proxy initialized")
	return p, nil
}

// setupCertManager configures the autocert manager for Let's Encrypt
func (p *Proxy) setupCertManager() {
	// Create a cache directory if it doesn't exist
	dir := p.config.CertDir
	if dir == "" {
		dir = p.app.GetConfig().General.StorageDir + "/certs"
	}

	// Set up the certificate manager
	certManager := &autocert.Manager{
		Prompt: autocert.AcceptTOS,
		Cache:  autocert.DirCache(dir),
	}

	// Configure the email if provided
	if p.config.Email != "" {
		certManager.Email = p.config.Email
	}

	// Configure the Let's Encrypt client
	if p.config.LetsEncryptMode == "staging" {
		certManager.Client = &acme.Client{
			DirectoryURL: "https://acme-staging-v02.api.letsencrypt.org/directory",
		}
	}

	// Set HostPolicy to allow the admin domain
	adminDomain := p.app.GetConfig().Http.FullDomain()
	certManager.HostPolicy = func(_ context.Context, host string) error {
		// Always allow the admin domain
		if host == adminDomain {
			return nil
		}

		// For other domains, check if they are in our routes
		p.mu.RLock()
		defer p.mu.RUnlock()

		if _, ok := p.routes[host]; ok {
			return nil
		}

		return fmt.Errorf("host %q not configured in gordon", host)
	}

	p.certManager = certManager
	log.Debug("Certificate manager setup completed",
		"directory", dir,
		"mode", p.config.LetsEncryptMode)

	// Request the certificate for the admin domain
	go p.requestAdminCertificate()
}

// requestAdminCertificate preemptively requests a Let's Encrypt certificate
// for the Gordon admin interface
func (p *Proxy) requestAdminCertificate() {
	adminDomain := p.app.GetConfig().Http.FullDomain()

	log.Info("Initiating Let's Encrypt certificate request for admin domain",
		"domain", adminDomain,
		"email", p.config.Email,
		"mode", p.config.LetsEncryptMode,
		"cert_dir", p.config.CertDir)

	// Set up an HTTP client that can be used to make a request to trigger certificate generation
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // We expect cert errors since we're creating the cert
			},
		},
		Timeout: 30 * time.Second,
	}

	// We need to make an HTTPS request to our domain to trigger the certificate generation
	// The first attempt will generate a certificate through Let's Encrypt
	url := fmt.Sprintf("https://%s", adminDomain)

	log.Debug("Sending HTTPS request to trigger certificate creation",
		"url", url)

	// Make the request
	resp, err := client.Get(url)
	if err != nil {
		log.Debug("Certificate request triggered with expected TLS error (this is normal)",
			"domain", adminDomain,
			"error", err.Error())

		// Check if the error message contains indications that DNS might not be configured
		if strings.Contains(err.Error(), "no such host") ||
			strings.Contains(err.Error(), "dial tcp") ||
			strings.Contains(err.Error(), "lookup") {
			log.Error("⚠️ DNS resolution failed for admin domain. Make sure the domain points to this server's IP address",
				"domain", adminDomain)
		}
	} else {
		resp.Body.Close()
		log.Info("Initial HTTPS request completed (unexpected)",
			"domain", adminDomain,
			"status", resp.StatusCode)
	}

	// Wait a bit for the certificate to be obtained
	log.Debug("Waiting for certificate generation process to complete")
	for i := 1; i <= 12; i++ { // Check for ~60 seconds (12 * 5s)
		time.Sleep(5 * time.Second)

		// Try to get the certificate from the cache
		hello := &tls.ClientHelloInfo{ServerName: adminDomain}
		cert, err := p.certManager.GetCertificate(hello)

		if err == nil && cert != nil && cert.Leaf != nil {
			log.Info("✅ Successfully obtained certificate for admin domain",
				"domain", adminDomain,
				"cert_expiry", cert.Leaf.NotAfter.Format("2006-01-02 15:04:05"),
				"issuer", cert.Leaf.Issuer.CommonName,
				"elapsed_time", fmt.Sprintf("%ds", i*5))
			return
		}

		log.Debug("Certificate not ready yet, continuing to wait",
			"attempt", i,
			"elapsed_time", fmt.Sprintf("%ds", i*5))
	}

	// Final check
	hello := &tls.ClientHelloInfo{ServerName: adminDomain}
	cert, err := p.certManager.GetCertificate(hello)

	if err != nil {
		log.Error("Failed to request certificate for admin domain after multiple attempts",
			"domain", adminDomain,
			"error", err)

		// Let's provide clear guidance about what might be wrong
		log.Error("⚠️ Certificate generation troubleshooting guide:")
		log.Error("1. Ensure DNS is correctly configured with the domain pointing to this server's IP")
		log.Error("2. Make sure port 80 is open and accessible from the internet for HTTP-01 challenge verification")
		log.Error("3. Check for rate limits from Let's Encrypt (https://letsencrypt.org/docs/rate-limits/)")
		log.Error("4. Verify that the domain belongs to you and is properly registered")
	} else if cert != nil && cert.Leaf != nil {
		log.Info("✅ Successfully obtained certificate for admin domain",
			"domain", adminDomain,
			"cert_expiry", cert.Leaf.NotAfter.Format("2006-01-02 15:04:05"),
			"issuer", cert.Leaf.Issuer.CommonName)
	} else {
		log.Warn("Certificate request in progress for admin domain - may take longer to complete",
			"domain", adminDomain)
		log.Info("You may need to restart the server once your DNS propagation is complete")
	}
}

// Start loads the routes from the database and starts the proxy server
func (p *Proxy) Start() error {
	// Check if there might be port conflicts with the main server
	mainServerPort := p.app.GetConfig().Http.Port

	// Check HTTP port conflict
	if p.config.HttpPort == mainServerPort {
		log.Warn("HTTP port for reverse proxy conflicts with main server port",
			"port", p.config.HttpPort,
			"solution", "reverse proxy HTTP server will be disabled")
		return nil
	}

	// Check HTTPS port conflict (less common, but still possible)
	if p.config.Port == mainServerPort {
		log.Warn("HTTPS port for reverse proxy conflicts with main server port",
			"port", p.config.Port,
			"solution", "reverse proxy HTTPS server will be disabled")
		return nil
	}

	// Load routes from the database
	if err := p.loadRoutes(); err != nil {
		return fmt.Errorf("failed to load routes: %w", err)
	}

	// Add a special route for the admin domain (Gordon itself)
	adminDomain := p.app.GetConfig().Http.FullDomain()

	// In a container environment, we need to use the host container IP
	// instead of 127.0.0.1 because each container has its own localhost
	containerIP := "host.containers.internal" // Modern Docker/Podman default hostname for host

	// Fall back options for container IP
	if os.Getenv("GORDON_ADMIN_HOST") != "" {
		// Allow explicit configuration via env var
		containerIP = os.Getenv("GORDON_ADMIN_HOST")
		log.Debug("Using admin host from environment variable",
			"host", containerIP)
	} else if os.Getenv("HOSTNAME") != "" {
		// Use container's own hostname as they're on the same network
		containerIP = os.Getenv("HOSTNAME")
		log.Debug("Using container hostname for admin routing",
			"hostname", containerIP)
	}

	// Try to detect the optimal Gordon admin host by testing connections
	testedIP := p.testAdminConnection(containerIP, p.app.GetConfig().Http.Port)
	if testedIP != "" && testedIP != containerIP {
		log.Info("Auto-detected working connection to Gordon admin",
			"host", testedIP,
			"original", containerIP)
		containerIP = testedIP
	}

	p.mu.Lock()
	// Only add if it doesn't already exist
	if _, exists := p.routes[adminDomain]; !exists {
		p.routes[adminDomain] = &ProxyRouteInfo{
			Domain:        adminDomain,
			ContainerIP:   containerIP,
			ContainerPort: p.app.GetConfig().Http.Port,
			ContainerID:   "gordon-server",
			Protocol:      "http", // Gordon server uses HTTP internally
			Path:          "/",
			Active:        true,
		}
		log.Info("Added special route for admin domain",
			"domain", adminDomain,
			"target", fmt.Sprintf("http://%s:%s", containerIP, p.app.GetConfig().Http.Port))
	}
	p.mu.Unlock()

	// Configure the proxy routes
	p.configureRoutes()

	// Configure HTTP server to handle Let's Encrypt HTTP-01 challenges
	// and redirect everything else to HTTPS
	p.httpServer.Any("/.well-known/acme-challenge/*", echo.WrapHandler(p.certManager.HTTPHandler(nil)))

	// Handle all other HTTP requests by redirecting to HTTPS
	p.httpServer.Any("/*", func(c echo.Context) error {
		// Skip handling acme challenges (already handled above)
		if strings.HasPrefix(c.Request().URL.Path, "/.well-known/acme-challenge/") {
			return nil
		}

		// Get client IP
		clientIP := c.RealIP()

		// Check if IP is blacklisted (if blacklist exists)
		if p.blacklist != nil && p.blacklist.IsBlocked(clientIP) {
			// Mark request as blacklisted to skip logging
			if reqCtx, ok := c.Get("reqContext").(*requestContext); ok {
				reqCtx.blacklisted = true
			}

			p.logBlockedIP(clientIP, c.Request().URL.Path, c.Request().UserAgent())
			return c.String(http.StatusForbidden, "Forbidden")
		}

		host := c.Request().Host
		if strings.Contains(host, ":") {
			host = strings.Split(host, ":")[0]
		}

		// Redirect to HTTPS
		return c.Redirect(http.StatusMovedPermanently,
			fmt.Sprintf("https://%s%s", host, c.Request().RequestURI))
	})

	// Start the HTTPS server
	httpsServer := &http.Server{
		Addr:    ":" + p.config.Port,
		Handler: p.httpsServer,
		TLSConfig: &tls.Config{
			GetCertificate: p.certManager.GetCertificate,
			MinVersion:     tls.VersionTLS12,
		},
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	// Start the HTTP server (for redirects and Let's Encrypt challenges)
	httpServer := &http.Server{
		Addr:         ":" + p.config.HttpPort,
		Handler:      p.httpServer,
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	p.serverStarted = true

	// Start the HTTPS server
	go func() {
		log.Info("Starting HTTPS reverse proxy server", "port", p.config.Port)
		if err := httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Error("Failed to start HTTPS reverse proxy server", "err", err)
		}
	}()

	// Start the HTTP server (for redirects and Let's Encrypt challenges)
	go func() {
		log.Info("Starting HTTP reverse proxy server (for redirects and Let's Encrypt challenges)", "port", p.config.HttpPort)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("Failed to start HTTP reverse proxy server", "err", err)
		}
	}()

	return nil
}

// testAdminConnection attempts to find a working connection to the Gordon admin
// by testing different hostnames and IPs
func (p *Proxy) testAdminConnection(defaultHost string, port string) string {
	log.Debug("Testing connections to Gordon admin server")

	// Create a client with a short timeout to quickly test connections
	client := &http.Client{
		Timeout: 1 * time.Second,
	}

	// Define addresses to try (from most likely to least likely)
	addresses := []string{
		defaultHost,                // Try the provided default first
		"localhost",                // Local connections
		"127.0.0.1",                // Localhost IP
		"host.docker.internal",     // Docker host
		"host.containers.internal", // Podman/Docker host
		"172.17.0.1",               // Common Docker gateway
		os.Getenv("HOSTNAME"),      // Container's own hostname
		"gordon",                   // Service name
	}

	// Remove duplicates and empty entries
	uniqueAddresses := make([]string, 0, len(addresses))
	seen := make(map[string]bool)

	for _, addr := range addresses {
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		uniqueAddresses = append(uniqueAddresses, addr)
	}

	// Test each address
	for _, addr := range uniqueAddresses {
		url := fmt.Sprintf("http://%s:%s/admin/ping", addr, port)
		log.Debug("Testing connection to Gordon admin", "url", url)

		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				log.Debug("Successfully connected to Gordon admin",
					"host", addr,
					"status", resp.StatusCode)
				return addr
			}
			log.Debug("Received error status from Gordon admin",
				"host", addr,
				"status", resp.StatusCode)
		} else {
			log.Debug("Failed to connect to Gordon admin",
				"host", addr,
				"error", err.Error())
		}
	}

	// If none worked, return the default
	return defaultHost
}

// Stop stops the proxy server
func (p *Proxy) Stop() error {
	if !p.serverStarted {
		return nil
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(p.config.GracePeriod)*time.Second,
	)
	defer cancel()

	// Shutdown the servers
	log.Info("Stopping reverse proxy servers")

	// Stop HTTPS server
	if err := p.httpsServer.Shutdown(ctx); err != nil {
		log.Error("Error stopping HTTPS server", "err", err)
	}

	// Stop HTTP server
	if err := p.httpServer.Shutdown(ctx); err != nil {
		log.Error("Error stopping HTTP server", "err", err)
	}

	return nil
}

// loadRoutes loads the routes from the database
func (p *Proxy) loadRoutes() error {
	// Lock the routes map
	p.mu.Lock()
	defer p.mu.Unlock()

	log.Debug("Loading proxy routes from database")

	// Query the database for active proxy routes
	rows, err := p.app.GetDB().Query(`
		SELECT pr.id, d.name, pr.container_id, pr.container_ip, pr.container_port, pr.protocol, pr.path, pr.active
		FROM proxy_route pr
		JOIN domain d ON pr.domain_id = d.id
		WHERE pr.active = 1
	`)
	if err != nil {
		if err == sql.ErrNoRows {
			log.Info("No active proxy routes found")
			return nil
		}
		return fmt.Errorf("failed to query database: %w", err)
	}
	defer rows.Close()

	// Clear the routes map
	p.routes = make(map[string]*ProxyRouteInfo)

	// Populate the routes map
	for rows.Next() {
		var id, domain, containerID, containerIP, containerPort, protocol, path string
		var active bool
		if err := rows.Scan(&id, &domain, &containerID, &containerIP, &containerPort, &protocol, &path, &active); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		// Add the route to the map
		p.routes[domain] = &ProxyRouteInfo{
			Domain:        domain,
			ContainerID:   containerID,
			ContainerIP:   containerIP,
			ContainerPort: containerPort,
			Protocol:      protocol,
			Path:          path,
			Active:        active,
		}

		log.Debug("Loaded proxy route",
			"domain", domain,
			"containerIP", containerIP,
			"containerPort", containerPort,
		)
	}

	log.Info("Loaded proxy routes", "count", len(p.routes))
	return nil
}

// configureRoutes sets up the echo routes for proxying
func (p *Proxy) configureRoutes() {
	// Lock the routes map for reading
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Log all available routes for debugging
	log.Debug("Configuring proxy routes")
	for domain, route := range p.routes {
		log.Debug("Configured route",
			"domain", domain,
			"target", fmt.Sprintf("%s://%s:%s", route.Protocol, route.ContainerIP, route.ContainerPort),
			"active", route.Active)
	}

	// Add a handler for all incoming requests (HTTPS)
	p.httpsServer.Any("/*", func(c echo.Context) error {
		// Get client IP
		clientIP := c.RealIP()

		// Check if IP is blacklisted (if blacklist exists)
		if p.blacklist != nil && p.blacklist.IsBlocked(clientIP) {
			// Mark request as blacklisted to skip logging
			if reqCtx, ok := c.Get("reqContext").(*requestContext); ok {
				reqCtx.blacklisted = true
			}

			p.logBlockedIP(clientIP, c.Request().URL.Path, c.Request().UserAgent())
			return c.String(http.StatusForbidden, "Forbidden")
		}

		host := c.Request().Host

		// Strip the port from the host
		if strings.Contains(host, ":") {
			host = strings.Split(host, ":")[0]
		}

		log.Debug("Processing request",
			"host", host,
			"path", c.Request().URL.Path,
			"method", c.Request().Method)

		// Find the route for this host
		route, ok := p.routes[host]
		if !ok {
			// Create list of available domains for debugging
			availableDomains := make([]string, 0, len(p.routes))
			for d := range p.routes {
				availableDomains = append(availableDomains, d)
			}

			log.Warn("Domain not found in routes",
				"domain", host,
				"available_domains", strings.Join(availableDomains, ", "))
			return c.String(http.StatusNotFound, "Domain not found")
		}

		// Check if the route is active
		if !route.Active {
			log.Warn("Route is not active", "domain", host)
			return c.String(http.StatusServiceUnavailable, "Route is not active")
		}

		// Special handling for the admin domain
		adminDomain := p.app.GetConfig().Http.FullDomain()
		if host == adminDomain {
			log.Debug("Proxying request to admin domain",
				"domain", host,
				"target", fmt.Sprintf("%s://%s:%s", route.Protocol, route.ContainerIP, route.ContainerPort),
				"path", c.Request().URL.Path)
		}

		// Create the target URL
		targetURL := &url.URL{
			Scheme: route.Protocol,
			Host:   fmt.Sprintf("%s:%s", route.ContainerIP, route.ContainerPort),
		}

		// Create a reverse proxy
		proxy := httputil.NewSingleHostReverseProxy(targetURL)

		// Update headers to allow for SSL redirection
		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			req.Header.Set("X-Forwarded-Proto", "https")
			req.Header.Set("X-Forwarded-Host", host)
			req.Header.Set("X-Forwarded-For", c.RealIP())
			req.Header.Set("X-Real-IP", c.RealIP())

			// Debug information
			log.Debug("Proxying request",
				"host", host,
				"target", targetURL.String(),
				"path", req.URL.Path,
				"clientIP", c.RealIP())
		}

		// Add error handling
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Error("Proxy error",
				"host", host,
				"path", r.URL.Path,
				"error", err)
			c.String(http.StatusBadGateway, "Proxy Error: "+err.Error())
		}

		// Serve the request
		proxy.ServeHTTP(c.Response(), c.Request())
		return nil
	})
}

// Reload reloads the routes from the database and reconfigures the proxy
func (p *Proxy) Reload() error {
	// Load routes from the database
	if err := p.loadRoutes(); err != nil {
		return fmt.Errorf("failed to load routes: %w", err)
	}

	// Reconfigure the routes
	p.configureRoutes()

	return nil
}

// AddRoute adds a new route to the database and reloads the proxy
func (p *Proxy) AddRoute(domainName, containerID, containerIP, containerPort, protocol, path string) error {
	// Begin a transaction
	tx, err := p.app.GetDB().Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Check if the domain exists
	var domainID string
	err = tx.QueryRow("SELECT id FROM domain WHERE name = ?", domainName).Scan(&domainID)
	if err != nil {
		if err == sql.ErrNoRows {
			// Domain doesn't exist, create it
			domainID = generateUUID()
			now := time.Now().Format(time.RFC3339)
			_, err = tx.Exec(
				"INSERT INTO domain (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)",
				domainID, domainName, now, now,
			)
			if err != nil {
				return fmt.Errorf("failed to insert domain: %w", err)
			}
		} else {
			return fmt.Errorf("failed to query domain: %w", err)
		}
	}

	// Check if a route already exists for this domain
	var existingRouteID string
	err = tx.QueryRow("SELECT id FROM proxy_route WHERE domain_id = ?", domainID).Scan(&existingRouteID)
	if err == nil {
		// Route exists, update it
		now := time.Now().Format(time.RFC3339)
		_, err = tx.Exec(
			`UPDATE proxy_route SET 
				container_id = ?, 
				container_ip = ?, 
				container_port = ?, 
				protocol = ?, 
				path = ?, 
				active = ?, 
				updated_at = ? 
			WHERE id = ?`,
			containerID, containerIP, containerPort, protocol, path, true, now, existingRouteID,
		)
		if err != nil {
			return fmt.Errorf("failed to update route: %w", err)
		}
	} else if err == sql.ErrNoRows {
		// Route doesn't exist, create it
		routeID := generateUUID()
		now := time.Now().Format(time.RFC3339)
		_, err = tx.Exec(
			`INSERT INTO proxy_route (
				id, domain_id, container_id, container_ip, container_port, 
				protocol, path, active, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			routeID, domainID, containerID, containerIP, containerPort,
			protocol, path, true, now, now,
		)
		if err != nil {
			return fmt.Errorf("failed to insert route: %w", err)
		}
	} else {
		return fmt.Errorf("failed to query route: %w", err)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Reload the routes
	if err := p.Reload(); err != nil {
		return fmt.Errorf("failed to reload routes: %w", err)
	}

	log.Info("Added proxy route",
		"domain", domainName,
		"containerIP", containerIP,
		"containerPort", containerPort,
	)
	return nil
}

// RemoveRoute removes a route from the database and reloads the proxy
func (p *Proxy) RemoveRoute(domainName string) error {
	// Begin a transaction
	tx, err := p.app.GetDB().Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Get the domain ID
	var domainID string
	err = tx.QueryRow("SELECT id FROM domain WHERE name = ?", domainName).Scan(&domainID)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("domain not found: %s", domainName)
		}
		return fmt.Errorf("failed to query domain: %w", err)
	}

	// Delete the route
	_, err = tx.Exec("DELETE FROM proxy_route WHERE domain_id = ?", domainID)
	if err != nil {
		return fmt.Errorf("failed to delete route: %w", err)
	}

	// Delete the domain
	_, err = tx.Exec("DELETE FROM domain WHERE id = ?", domainID)
	if err != nil {
		return fmt.Errorf("failed to delete domain: %w", err)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Reload the routes
	if err := p.Reload(); err != nil {
		return fmt.Errorf("failed to reload routes: %w", err)
	}

	log.Info("Removed proxy route", "domain", domainName)
	return nil
}

// generateUUID generates a UUID for use as a primary key
func generateUUID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// joinPaths joins two paths ensuring there's only one slash between them
func joinPaths(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if a[len(a)-1] == '/' {
		a = a[:len(a)-1]
	}
	if b[0] == '/' {
		b = b[1:]
	}
	return a + "/" + b
}

// GetRoutes returns a copy of the routes map
func (p *Proxy) GetRoutes() map[string]*ProxyRouteInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Create a copy of the routes map
	routes := make(map[string]*ProxyRouteInfo, len(p.routes))
	for k, v := range p.routes {
		routes[k] = v
	}

	return routes
}

// Add a helper method to log blocked IPs with rate limiting
func (p *Proxy) logBlockedIP(clientIP, path, userAgent string) {
	p.blockedIPCountMu.Lock()
	defer p.blockedIPCountMu.Unlock()

	now := time.Now()

	// If this is the first block or it's been more than 5 minutes since the last summary
	if p.lastBlockedLog.IsZero() || now.Sub(p.lastBlockedLog) > 5*time.Minute {
		// Log this block and reset counters
		log.Info("Blocked request from blacklisted IP",
			"ip", clientIP,
			"path", path,
			"user_agent", userAgent)

		// Reset counters
		p.blockedIPCounter = make(map[string]int)
		p.blockedIPCounter[clientIP] = 1
		p.lastBlockedLog = now
		return
	}

	// Increment counter for this IP
	p.blockedIPCounter[clientIP]++

	// If it's been at least 60 seconds since the last log, print a summary
	if now.Sub(p.lastBlockedLog) >= 60*time.Second {
		// Log summary of blocked requests
		totalBlocked := 0
		for _, count := range p.blockedIPCounter {
			totalBlocked += count
		}

		log.Info("Blocked IP summary",
			"unique_ips", len(p.blockedIPCounter),
			"total_requests", totalBlocked,
			"since", p.lastBlockedLog.Format(time.RFC3339))

		// Reset counters
		p.blockedIPCounter = make(map[string]int)
		p.lastBlockedLog = now
	}
}
