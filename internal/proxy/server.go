package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"net"

	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/acme"
)

// Global mutex for admin connection testing to prevent concurrent executions
var adminTestMutex sync.Mutex

// This file contains server operations functions for the proxy.
// These methods may need to be deleted from proxy.go after validation.

// Start loads the routes from the database and starts the proxy server
func (p *Proxy) Start() error {
	// Check if proxy is disabled via configuration
	if !p.config.Enabled {
		logger.Info("Proxy server is disabled via configuration, not starting")
		return nil
	}

	// Configure proxy ports based on environment
	p.configureProxyPorts()

	// Load routes from the database
	if err := p.loadRoutes(); err != nil {
		return fmt.Errorf("failed to load routes: %w", err)
	}

	// Log Gordon container identity and prepare admin route
	p.LogGordonIdentity()
	p.setupAdminRoute()

	// Scan containers for certificates
	if err := p.scanContainersAndCheckCertificates(); err != nil {
		logger.Warn("Failed to scan containers for certificate checks", "error", err)
	}

	// Discover missing routes and log active containers
	p.DiscoverMissingRoutes()
	p.LogActiveContainersAndRoutes()

	// Start container event listener
	if err := p.StartContainerEventListener(); err != nil {
		logger.Warn("Failed to start container event listener", "error", err)
	}

	// Start the admin route verification in a separate goroutine
	go p.StartAdminRouteVerification()

	// Check for port conflicts with the main server
	if p.checkForPortConflicts() {
		return nil
	}

	// Set up middleware and configure routes
	p.setupMiddleware()
	p.configureRoutes()

	// Configure HTTP server for Let's Encrypt challenges
	p.httpServer.Any("/.well-known/acme-challenge/*", echo.WrapHandler(p.certManager.HTTPHandler(nil)))

	// Create and start HTTP and HTTPS servers
	p.serverStarted = true

	// Start servers in separate goroutines
	go p.startHTTPSServer()
	go p.startHTTPServer()

	// Log details about the proxy server
	logger.Info("Proxy server started",
		"http_port", p.config.HttpPort,
		"https_port", p.config.Port,
		"admin_domain", p.app.GetConfig().Http.FullDomain(),
		"container_id", p.gordonContainerID,
	)

	// Give servers time to start up
	logger.Info("Waiting for servers to complete startup...")
	time.Sleep(3 * time.Second)

	// Test admin connection after server starts
	go p.TestAdminConnectionLater()

	return nil
}

// configureProxyPorts configures the proxy ports based on the environment
func (p *Proxy) configureProxyPorts() {
	// Detect container environment
	containerEnv := os.Getenv("container")
	_, isContainer := os.LookupEnv("container")

	// Log container information if applicable
	if isContainer {
		logger.Info("Running in container environment", "container_type", containerEnv, "uid", os.Getuid())
	}

	// When running as non-root user, check for privileged ports
	if !isContainer && os.Getuid() != 0 {
		// Non-root user - check for privileged ports
		httpPort, _ := strconv.Atoi(p.config.HttpPort)
		httpsPort, _ := strconv.Atoi(p.config.Port)

		// Use non-privileged ports if trying to bind to privileged ports without root
		if httpPort < 1024 {
			logger.Warn("Using alternative HTTP port 8080 instead of privileged port", "original", p.config.HttpPort)
			p.config.HttpPort = "8080"
		}

		if httpsPort < 1024 {
			logger.Warn("Using alternative HTTPS port 8443 instead of privileged port", "original", p.config.Port)
			p.config.Port = "8443"
		}
	}

	// Log final port configuration
	logger.Info("Final proxy port configuration",
		"http_port", p.config.HttpPort,
		"https_port", p.config.Port,
		"in_container", isContainer)
}

// setupAdminRoute adds a special route for the admin domain (Gordon itself)
func (p *Proxy) setupAdminRoute() {
	adminDomain := p.app.GetConfig().Http.FullDomain()
	logger.Info("Setting up admin domain route", "domain", adminDomain)

	// Auto-detect the Gordon container name and ID
	containerName := p.detectGordonContainer()
	networkName := p.app.GetConfig().ContainerEngine.Network

	// Get the IP address from the container's network
	if p.gordonContainerID == "" {
		logger.Error("Failed to detect Gordon container ID, cannot add admin route")
		return
	}

	// Get the IP address from the container's network
	ip, err := docker.GetContainerIPFromNetwork(p.gordonContainerID, networkName)
	if err != nil || ip == "" {
		logger.Error("Failed to get Gordon container IP from network",
			"container_id", p.gordonContainerID,
			"network", networkName,
			"error", err)
		return
	}

	// Add the admin route to the database
	err = p.AddRoute(
		adminDomain,
		containerName,
		ip,
		p.app.GetConfig().Http.Port,
		"http", // Gordon server uses HTTP internally
		"/",
	)

	if err != nil {
		logger.Error("Failed to add admin route to database", "error", err, "domain", adminDomain)
	} else {
		logger.Info("Ensured admin domain route is in database", "domain", adminDomain)
	}

	// Add to in-memory routes if it doesn't already exist
	p.mu.Lock()
	if _, exists := p.routes[adminDomain]; !exists {
		p.routes[adminDomain] = &ProxyRouteInfo{
			Domain:        adminDomain,
			ContainerIP:   ip,
			ContainerPort: p.app.GetConfig().Http.Port,
			ContainerID:   containerName,
			Protocol:      "http", // Gordon server uses HTTP internally
			Path:          "/",
			Active:        true,
		}
		logger.Info("Added special route for admin domain", "domain", adminDomain)
	}
	p.mu.Unlock()
}

// checkForPortConflicts checks if there are port conflicts with the main server
// Returns true if conflicts are found and the proxy should not be started
func (p *Proxy) checkForPortConflicts() bool {
	mainServerPort := p.app.GetConfig().Http.Port

	// Check HTTP port conflict
	if p.config.HttpPort == mainServerPort {
		logger.Warn("HTTP port for reverse proxy conflicts with main server port",
			"port", p.config.HttpPort,
			"solution", "reverse proxy HTTP server will be disabled")
		return true
	}

	// Check HTTPS port conflict
	if p.config.Port == mainServerPort {
		logger.Warn("HTTPS port for reverse proxy conflicts with main server port",
			"port", p.config.Port,
			"solution", "reverse proxy HTTPS server will be disabled")
		return true
	}

	return false
}

// startHTTPSServer starts the HTTPS server
func (p *Proxy) startHTTPSServer() {
	// Create HTTPS server
	httpsServer := &http.Server{
		Addr:    fmt.Sprintf(":%s", p.config.Port),
		Handler: p.httpsServer,
		TLSConfig: &tls.Config{
			GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				// Try to get the certificate from the autocert manager
				cert, err := p.certManager.GetCertificate(hello)

				// If no cert available and we have a fallback, use it for admin domain
				adminDomain := p.app.GetConfig().Http.FullDomain()
				if (err != nil || cert == nil) && hello.ServerName == adminDomain && p.fallbackCert != nil {
					logger.Info("Using fallback certificate for admin domain", "domain", adminDomain)
					return p.fallbackCert, nil
				}

				return cert, err
			},
			MinVersion: tls.VersionTLS12,
			ServerName: p.app.GetConfig().Http.FullDomain(),
			NextProtos: []string{acme.ALPNProto},
		},
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
		ErrorLog:     log.New(os.Stderr, "[HTTPS] ", log.LstdFlags),
	}

	// Try to start the server with different approaches depending on environment
	logger.Info("Starting HTTPS server", "port", p.config.Port)

	// First try explicit IPv4 binding
	httpsAddr := fmt.Sprintf("0.0.0.0:%s", p.config.Port)
	tcpListener, err := net.Listen("tcp4", httpsAddr)

	if err != nil {
		logger.Warn("Failed to create HTTPS listener with explicit IPv4 binding",
			"error", err, "port", p.config.Port)

		// Try simple binding as fallback
		if err := httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTPS server failed", "error", err, "port", p.config.Port)
		}
		return
	}

	// Create TLS listener
	tlsListener := tls.NewListener(tcpListener, httpsServer.TLSConfig)
	logger.Info("HTTPS server is now accepting connections", "port", p.config.Port)

	// Start server with explicit listener
	if err := httpsServer.Serve(tlsListener); err != nil && err != http.ErrServerClosed {
		logger.Error("HTTPS server failed", "error", err, "port", p.config.Port)
	}
}

// startHTTPServer starts the HTTP server
func (p *Proxy) startHTTPServer() {
	// Create HTTP server
	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%s", p.config.HttpPort),
		Handler:      p.httpServer,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
		ErrorLog:     log.New(os.Stderr, "[HTTP] ", log.LstdFlags),
	}

	// Try to start the server with different approaches depending on environment
	logger.Info("Starting HTTP server", "port", p.config.HttpPort)

	// First try explicit IPv4 binding
	httpAddr := fmt.Sprintf("0.0.0.0:%s", p.config.HttpPort)
	httpListener, err := net.Listen("tcp4", httpAddr)

	if err != nil {
		logger.Warn("Failed to create HTTP listener with explicit IPv4 binding",
			"error", err, "port", p.config.HttpPort)

		// Try simple binding as fallback
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server failed", "error", err, "port", p.config.HttpPort)
		}
		return
	}

	// Start server with explicit listener
	logger.Info("HTTP server is now accepting connections", "port", p.config.HttpPort)
	if err := httpServer.Serve(httpListener); err != nil && err != http.ErrServerClosed {
		logger.Error("HTTP server failed", "error", err, "port", p.config.HttpPort)
	}
}

// Stop gracefully shuts down the proxy server
func (p *Proxy) Stop() error {
	logger.Info("Stopping reverse proxy")

	// Check if proxy is disabled via configuration
	if !p.config.Enabled {
		logger.Info("Proxy server is disabled via configuration, nothing to stop")
		return nil
	}

	// Run any rate limiter cleanup functions
	for _, cleanup := range rateLimiterCleanup {
		cleanup()
	}

	// Stop the container event listener
	docker.StopContainerEventListener()

	// Skip the rest if the server was never started
	if !p.serverStarted {
		logger.Debug("Proxy server was never started, nothing to stop")
		return nil
	}

	// Create a context with timeout for shutdown
	timeout := p.config.GracePeriod
	if timeout <= 0 {
		timeout = 10 // Default to 10 seconds if not set
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	// Shutdown HTTP server if it's running
	if p.httpServer != nil {
		if err := p.httpServer.Shutdown(ctx); err != nil {
			logger.Error("Failed to gracefully shutdown HTTP server", "error", err)
		}
	}

	// Shutdown HTTPS server if it's running
	if p.httpsServer != nil {
		if err := p.httpsServer.Shutdown(ctx); err != nil {
			logger.Error("Failed to gracefully shutdown HTTPS server", "error", err)
		}
	}

	logger.Info("Reverse proxy stopped")
	return nil
}

// testAdminConnection attempts to find a working connection to the Gordon admin
func (p *Proxy) testAdminConnection(defaultHost string, port string) string {
	adminPath := p.app.GetConfig().Admin.Path
	url := fmt.Sprintf("http://%s:%s%s", defaultHost, port, adminPath)

	logger.Debug("Testing admin connection", "url", url)

	// Try connecting using the provided default host
	client := &http.Client{
		// Use a simple TLS config instead of getting it from the app
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get(url)
	if err == nil {
		resp.Body.Close()
		logger.Info("Admin connection successful", "host", defaultHost)
		return defaultHost
	}

	// If connection failed, try other common hostnames
	hosts := []string{
		"localhost",
		"127.0.0.1",
		"host.docker.internal",
		"host.containers.internal",
	}

	for _, host := range hosts {
		if host == defaultHost {
			continue // Already tried this one
		}

		url = fmt.Sprintf("http://%s:%s%s", host, port, adminPath)
		logger.Debug("Trying alternative host", "url", url)

		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			logger.Info("Found working admin connection", "host", host)
			return host
		}
	}

	logger.Warn("Could not establish admin connection on any tested host", "fallback", defaultHost)
	return defaultHost
}

// TestAdminConnectionLater tests the admin connection after a short delay
func (p *Proxy) TestAdminConnectionLater() {
	// Try to acquire the lock, return immediately if another test is in progress
	if !adminTestMutex.TryLock() {
		logger.Debug("Admin connection test already in progress, skipping")
		return
	}
	defer adminTestMutex.Unlock()

	// Wait for servers to start - give plenty of time for binding to complete
	time.Sleep(10 * time.Second)

	// Log that we're beginning the connection test
	logger.Info("Beginning admin connection test after server startup")

	// First verify if ports 80 and 443 are actually bound locally
	verifyLocalPortBinding := func(port string) bool {
		// Try a local connection to the port
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%s", port), 2*time.Second)
		if err != nil {
			logger.Error(fmt.Sprintf("Failed to connect to localhost:%s - port may not be bound", port),
				"error", err)
			return false
		}
		conn.Close()
		logger.Info(fmt.Sprintf("Successfully verified port %s is bound locally", port))
		return true
	}

	// Check if we can connect to ports 80 and 443 locally
	httpBound := verifyLocalPortBinding(p.config.HttpPort)
	httpsBound := verifyLocalPortBinding(p.config.Port)

	if !httpBound || !httpsBound {
		logger.Error("One or more ports are not bound locally - proxy may not be functioning",
			"http_port_ok", httpBound,
			"https_port_ok", httpsBound)
	}

	logger.Debug("Testing admin connections after server startup")

	// Get domain and current route
	adminDomain := p.app.GetConfig().Http.FullDomain()

	// First, check if the route exists in memory
	p.mu.RLock()
	route, exists := p.routes[adminDomain]
	p.mu.RUnlock()

	// Add debug logging for admin route configuration
	if exists {
		logger.Debug("Current admin route configuration",
			"domain", adminDomain,
			"container_ip", route.ContainerIP,
			"container_port", route.ContainerPort,
			"protocol", route.Protocol)

		// Get the container's own IP by checking network interfaces
		containerIP := p.getOwnContainerIP()
		adminPath := p.app.GetConfig().Admin.Path
		url := fmt.Sprintf("http://%s:%s%s/ping", containerIP, route.ContainerPort, adminPath)

		logger.Debug("Testing direct IP connection", "url", url)
		client := &http.Client{
			Timeout: 5 * time.Second,
		}

		resp, err := client.Get(url)
		if err != nil {
			logger.Error("Failed to connect to admin with direct IP",
				"url", url,
				"error", err)
		} else {
			resp.Body.Close()
			logger.Info("Successfully connected to admin with direct IP",
				"url", url,
				"status", resp.Status)

			// If localhost is being used, update the route to use the container IP
			if route.ContainerIP == "localhost" || route.ContainerIP == "127.0.0.1" {
				logger.Info("Updating admin route from localhost to container IP",
					"old_ip", route.ContainerIP,
					"new_ip", containerIP)

				p.mu.Lock()
				if r, ok := p.routes[adminDomain]; ok {
					r.ContainerIP = containerIP
				}
				p.mu.Unlock()

				// Reconfigure routes with the new IP
				p.configureRoutes()

				// Also update in database if possible
				err := p.ForceUpdateRouteIP(adminDomain, containerIP)
				if err != nil {
					logger.Warn("Failed to update admin route IP in database",
						"error", err,
						"domain", adminDomain)
				}
			}
		}
	}

	if !exists {
		logger.Warn("Admin route not found in memory, creating it now")
		// Auto-detect the Gordon container name and ID
		containerName := p.detectGordonContainer()

		// Get the network name from config
		networkName := p.app.GetConfig().ContainerEngine.Network

		// Try to get the actual container IP from the network
		if p.gordonContainerID == "" {
			logger.Error("Failed to detect Gordon container ID, cannot add admin route")
			return
		}

		// Get the IP address from the container's network
		ip, err := docker.GetContainerIPFromNetwork(p.gordonContainerID, networkName)
		if err != nil || ip == "" {
			logger.Error("Failed to get Gordon container IP from network",
				"container_id", p.gordonContainerID,
				"network", networkName,
				"error", err)
			ip = "localhost" // Fallback to localhost
		}

		// Create the route in the database
		err = p.AddRoute(
			adminDomain,
			containerName,
			ip,
			p.app.GetConfig().Http.Port,
			"http", // Gordon server uses HTTP internally
			"/",
		)

		if err != nil {
			logger.Error("Failed to add admin route to database during recovery",
				"error", err,
				"domain", adminDomain)
		} else {
			logger.Info("Successfully recreated admin domain route in database",
				"domain", adminDomain,
				"container", containerName,
				"target", fmt.Sprintf("http://%s:%s", ip, p.app.GetConfig().Http.Port))
		}

		// Add route to memory directly to ensure it's available immediately
		p.mu.Lock()
		p.routes[adminDomain] = &ProxyRouteInfo{
			Domain:        adminDomain,
			ContainerIP:   ip,
			ContainerPort: p.app.GetConfig().Http.Port,
			ContainerID:   containerName,
			Protocol:      "http", // Gordon server uses HTTP internally
			Path:          "/",
			Active:        true,
		}
		p.mu.Unlock()

		// Reconfigure routes to apply the changes
		p.configureRoutes()

		// Update route variable for the next steps
		p.mu.RLock()
		route, exists = p.routes[adminDomain]
		p.mu.RUnlock()

		if !exists {
			logger.Error("Failed to create admin route, route still doesn't exist after recreation attempt")
			return
		}
	}

	// Test connection and update if needed
	testedIP := p.testAdminConnection(route.ContainerIP, p.app.GetConfig().Http.Port)
	if testedIP != route.ContainerIP {
		p.mu.Lock()
		if r, ok := p.routes[adminDomain]; ok {
			r.ContainerIP = testedIP
			logger.Info("Updated admin route with working connection",
				"domain", adminDomain,
				"ip", testedIP)

			// Update the route in database as well
			err := p.ForceUpdateRouteIP(adminDomain, testedIP)
			if err != nil {
				logger.Warn("Failed to update admin route IP in database",
					"error", err,
					"domain", adminDomain)
			}
		}
		p.mu.Unlock()

		// Reconfigure routes to apply the IP change
		p.configureRoutes()
	}

	// Check external port access
	p.checkExternalPortAccess()
}

// checkPorts is a helper function to diagnose what might be using a port
func checkPorts(protocol, port string) {
	// This is just a debugging helper - errors are ignored
	// Try using netstat to check port usage
	output, err := exec.Command("netstat", "-tuln").CombinedOutput()
	if err == nil {
		logger.Debug("Network port status", "output", string(output))
	}

	// Try using lsof to check port usage
	output, err = exec.Command("lsof", "-i", fmt.Sprintf("%s:%s", protocol, port)).CombinedOutput()
	if err == nil && len(output) > 0 {
		logger.Debug("Processes using port", "port", port, "output", string(output))
	} else {
		logger.Debug("No process found using port with lsof", "port", port)
	}
}

// checkExternalPortAccess verifies if ports 80 and 443 are accessible
func (p *Proxy) checkExternalPortAccess() {
	adminDomain := p.app.GetConfig().Http.FullDomain()
	if adminDomain == "" {
		return
	}

	// Test HTTP port 80
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // Don't follow redirects
		},
	}

	httpURL := fmt.Sprintf("http://%s/.well-known/acme-challenge/test", adminDomain)
	_, httpErr := client.Get(httpURL)
	if httpErr != nil {
		logger.Warn("External HTTP port 80 might not be accessible",
			"error", httpErr.Error())
	} else {
		logger.Info("External HTTP port 80 is accessible")
	}

	// Test HTTPS port 443
	httpsClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	httpsURL := fmt.Sprintf("https://%s/", adminDomain)
	_, httpsErr := httpsClient.Get(httpsURL)
	if httpsErr != nil {
		logger.Warn("External HTTPS port 443 might not be accessible",
			"error", httpsErr.Error())
	} else {
		logger.Info("External HTTPS port 443 is accessible")
	}
}

// getOwnContainerIP attempts to determine the container's own IP address
// from network interfaces, preferring non-loopback interfaces
func (p *Proxy) getOwnContainerIP() string {
	// Default fallback IP based on common container network range
	defaultIP := "10.89.0.118" // Updated to match the observed IP in logs

	// Use environment variable if available (can be set in container config)
	if envIP := os.Getenv("GORDON_CONTAINER_IP"); envIP != "" {
		logger.Debug("Using container IP from environment variable", "ip", envIP)
		return envIP
	}

	// Try to get the IP from the Gordon network - this is the most reliable method
	networkName := p.app.GetConfig().ContainerEngine.Network
	if p.gordonContainerID != "" {
		ip, err := docker.GetContainerIPFromNetwork(p.gordonContainerID, networkName)
		if err == nil && ip != "" {
			logger.Info("Using container IP from Docker network", "ip", ip, "network", networkName)
			return ip
		} else {
			logger.Error("Failed to get container IP from Docker network",
				"error", err,
				"container_id", p.gordonContainerID,
				"network", networkName)
		}
	} else {
		logger.Error("Gordon container ID not set, cannot get container IP from Docker network")
	}

	// Try to detect by listing network interfaces
	ifaces, err := net.Interfaces()
	if err != nil {
		logger.Error("Failed to list network interfaces", "error", err)
		return defaultIP
	}

	// First, look for eth0 or similar standard interfaces
	for _, iface := range ifaces {
		// Skip loopback
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		// Prefer eth0 or similar
		if strings.HasPrefix(iface.Name, "eth") || strings.HasPrefix(iface.Name, "en") {
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}

			for _, addr := range addrs {
				ip, _, err := net.ParseCIDR(addr.String())
				if err != nil {
					continue
				}

				// Only use IPv4 addresses
				if ipv4 := ip.To4(); ipv4 != nil {
					logger.Debug("Found container IP from network interface",
						"interface", iface.Name,
						"ip", ipv4.String())
					return ipv4.String()
				}
			}
		}
	}

	// If no preferred interface found, use any non-loopback interface
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err != nil {
				continue
			}

			if ipv4 := ip.To4(); ipv4 != nil {
				logger.Debug("Found container IP from fallback interface",
					"interface", iface.Name,
					"ip", ipv4.String())
				return ipv4.String()
			}
		}
	}

	logger.Warn("Could not determine container IP, using default", "ip", defaultIP)
	return defaultIP
}

// StartAdminRouteVerification starts a periodic verification of admin routes
// to ensure they are correctly registered and functioning
func (p *Proxy) StartAdminRouteVerification() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	logger.Debug("Starting admin route verification service")

	for {
		select {
		case <-ticker.C:
			adminDomain := p.app.GetConfig().Http.FullDomain()

			// Check if the admin route exists in the routes map
			p.mu.RLock()
			_, exists := p.routes[adminDomain]
			p.mu.RUnlock()

			if !exists {
				logger.Warn("Admin route missing during periodic check, recreating it")
				p.TestAdminConnectionLater()
				continue
			}

			// Test the existing admin connection
			p.TestAdminConnectionLater()
		}
	}
}
