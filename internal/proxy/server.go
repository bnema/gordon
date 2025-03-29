package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/logger"
)

// This file contains server operations functions for the proxy.
// These methods may need to be deleted from proxy.go after validation.

// Start loads the routes from the database and starts the proxy server
func (p *Proxy) Start() error {
	// First, load routes from the database
	if err := p.loadRoutes(); err != nil {
		return fmt.Errorf("failed to load routes: %w", err)
	}

	// Now log Gordon container identity for debugging (after routes are loaded)
	p.LogGordonIdentity()

	// Scan Gordon network containers and check for certificates
	if err := p.scanContainersAndCheckCertificates(); err != nil {
		logger.Warn("Failed to scan containers for certificate checks", "error", err)
	}

	// Run an immediate route verification to ensure all routes are valid on startup
	logger.Info("Performing initial route verification on startup")

	// Discover any containers that should have routes but don't
	p.DiscoverMissingRoutes()

	// Log all active containers and their routes for visibility
	p.LogActiveContainersAndRoutes()

	// Start the container event listener for real-time updates
	if err := p.StartContainerEventListener(); err != nil {
		logger.Warn("Failed to start container event listener", "error", err)
		// Continue anyway, this is non-critical
	}

	// Check if there might be port conflicts with the main server
	mainServerPort := p.app.GetConfig().Http.Port

	// Check HTTP port conflict
	if p.config.HttpPort == mainServerPort {
		logger.Warn("HTTP port for reverse proxy conflicts with main server port",
			"port", p.config.HttpPort,
			"solution", "reverse proxy HTTP server will be disabled")
		return nil
	}

	// Check HTTPS port conflict (less common, but still possible)
	if p.config.Port == mainServerPort {
		logger.Warn("HTTPS port for reverse proxy conflicts with main server port",
			"port", p.config.Port,
			"solution", "reverse proxy HTTPS server will be disabled")
		return nil
	}

	// Add a special route for the admin domain (Gordon itself)
	adminDomain := p.app.GetConfig().Http.FullDomain()

	// Auto-detect the Gordon container name and ID
	containerName := p.detectGordonContainer()

	// Get the network name from config
	networkName := p.app.GetConfig().ContainerEngine.Network

	// Initialize container IP
	var containerIP string

	// Try to get the actual container IP from the network
	if p.gordonContainerID == "" {
		return fmt.Errorf("failed to detect Gordon container ID, cannot add admin route")
	}

	// Get the IP address from the container's network
	ip, err := docker.GetContainerIPFromNetwork(p.gordonContainerID, networkName)
	if err != nil || ip == "" {
		logger.Error("Failed to get Gordon container IP from network",
			"container_id", p.gordonContainerID,
			"network", networkName,
			"error", err)
		return fmt.Errorf("failed to get Gordon container IP from network %s: %w", networkName, err)
	}

	containerIP = ip
	logger.Info("Using Gordon container IP from network",
		"container_id", p.gordonContainerID,
		"network", networkName,
		"ip", containerIP)

	// Check if admin route exists in database and create/update it
	err = p.AddRoute(
		adminDomain,
		containerName,
		containerIP,
		p.app.GetConfig().Http.Port,
		"http", // Gordon server uses HTTP internally
		"/",
	)

	if err != nil {
		logger.Error("Failed to add admin route to database",
			"error", err,
			"domain", adminDomain)
	} else {
		logger.Info("Ensured admin domain route is in database",
			"domain", adminDomain,
			"container", containerName,
			"target", fmt.Sprintf("http://%s:%s", containerIP, p.app.GetConfig().Http.Port))
	}

	p.mu.Lock()
	// Only add if it doesn't already exist in memory
	if _, exists := p.routes[adminDomain]; !exists {
		p.routes[adminDomain] = &ProxyRouteInfo{
			Domain:        adminDomain,
			ContainerIP:   containerIP,
			ContainerPort: p.app.GetConfig().Http.Port,
			ContainerID:   containerName, // Use auto-detected container name
			Protocol:      "http",        // Gordon server uses HTTP internally
			Path:          "/",
			Active:        true,
		}
		logger.Info("Added special route for admin domain",
			"domain", adminDomain,
			"container", containerName,
			"target", fmt.Sprintf("http://%s:%s", containerIP, p.app.GetConfig().Http.Port))
	} else {
		logger.Debug("Admin domain route already exists",
			"domain", adminDomain,
			"route", fmt.Sprintf("%s://%s:%s",
				p.routes[adminDomain].Protocol,
				p.routes[adminDomain].ContainerIP,
				p.routes[adminDomain].ContainerPort))
	}
	p.mu.Unlock()

	// Explicitly request certificate for admin domain after ensuring route exists
	if err := p.requestDomainCertificate(adminDomain); err != nil {
		logger.Error("Failed to request initial certificate for admin domain", "domain", adminDomain, "error", err)
		// Don't return an error here, let the server start and retry later
	}

	// Set up middleware
	p.setupMiddleware()

	// Configure routes
	p.configureRoutes()

	// Configure HTTPS and HTTP servers using our refactored method
	if err := p.StartHTTPSServer(); err != nil {
		logger.Error("Failed to start HTTPS server", "error", err)
		return err
	}

	return nil
}

// Stop gracefully shuts down the proxy server
func (p *Proxy) Stop() error {
	logger.Info("Stopping reverse proxy")

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

	// Removed HTTP server shutdown logic
	// if p.httpServer != nil {
	// 	if err := p.httpServer.Shutdown(ctx); err != nil {
	// 		logger.Error("Failed to gracefully shutdown HTTP server", "error", err)
	// 	}
	// }

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
	// Wait for servers to start
	time.Sleep(2 * time.Second)

	logger.Debug("Testing admin connections after server startup")

	// Get domain and current route
	adminDomain := p.app.GetConfig().Http.FullDomain()

	p.mu.RLock()
	route, exists := p.routes[adminDomain]
	p.mu.RUnlock()

	if !exists {
		logger.Warn("Admin route not found")
		return
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
		}
		p.mu.Unlock()
	}

	// Check external port access
	p.checkExternalPortAccess()
}

// checkExternalPortAccess verifies if ports 80 and 443 are accessible
func (p *Proxy) checkExternalPortAccess() {
	adminDomain := p.app.GetConfig().Http.FullDomain()
	if adminDomain == "" {
		return
	}

	// Test HTTP port 80
	/* // Commenting out HTTP check due to potential hairpin NAT issues
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
	*/

	// Test HTTPS port 443
	/* // Commenting out httpsClient as it's no longer used
	httpsClient := &http.Client{
		Timeout: 10 * time.Second, // Increased timeout for more reliable testing
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
			// Add more detailed timeout settings
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			// Add a custom dialer with explicit timeouts
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
				DualStack: true,
			}).DialContext,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	*/

	// Verify TLS settings are consistent
	if p.certificateManager == nil { // Updated check
		logger.Error("Certificate manager is nil - TLS will not work correctly",
			"solution", "Check NewProxy implementation and logs")
	}

	/* // Commenting out port 443 checks due to potential hairpin NAT issues
	// First try a basic TCP connection to port 443 to test if it's open
	logger.Info("Testing TCP connectivity to HTTPS port 443...")
	conn, tcpErr := net.DialTimeout("tcp", adminDomain+":443", 5*time.Second)
	if tcpErr != nil {
		logger.Warn("TCP connection to port 443 failed - HTTPS will not work",
			"domain", adminDomain,
			"error", tcpErr.Error())
	} else {
		conn.Close()
		logger.Info("TCP connection to port 443 successful")
	}

	// Then try the full HTTPS request
	httpsURL := fmt.Sprintf("https://%s/", adminDomain)
	logger.Info("Testing HTTPS connection with SNI...", "url", httpsURL)
	_, httpsErr := httpsClient.Get(httpsURL)
	if httpsErr != nil {
		logger.Warn("External HTTPS port 443 might not be accessible",
			"error", httpsErr.Error())

		// Provide more detailed troubleshooting info
		if strings.Contains(httpsErr.Error(), "certificate") {
			logger.Warn("TLS certificate issue detected",
				"hint", "Certificate verification failed but connection succeeded")
		} else if strings.Contains(httpsErr.Error(), "connection refused") {
			logger.Warn("Connection refused - firewall may be blocking port 443")
		} else if strings.Contains(httpsErr.Error(), "i/o timeout") || strings.Contains(httpsErr.Error(), "deadline exceeded") {
			logger.Warn("Connection timeout - port 443 may be filtered or not properly forwarded")
		}
	} else {
		logger.Info("External HTTPS port 443 is accessible - TLS handshake successful")
	}
	*/
}

// // detectGordonContainer attempts to find the Gordon container automatically
// func (p *Proxy) detectGordonContainer() string {
// 	// Check if we're running inside a container
// 	if docker.IsRunningInContainer() {
// 		logger.Debug("Detected we're running in a container")

// 		// Get our hostname which should be the container ID in Docker/Podman
// 		hostname := os.Getenv("HOSTNAME")
// 		if hostname != "" {
// 			// Check if there's a container with this hostname
// 			containers, err := docker.ListRunningContainers()
// 			if err == nil {
// 				for _, container := range containers {
// 					// Check if this container is us by comparing hostname with container ID
// 					if strings.HasPrefix(hostname, container.ID) {
// 						// This is our container
// 						containerName := strings.TrimLeft(container.Names[0], "/")
// 						logger.Debug("Auto-detected Gordon container name from hostname", "container_name", containerName)
// 						return containerName
// 					}
// 				}
// 			} else {
// 				logger.Debug("Could not list containers when checking hostname", "error", err)
// 			}
// 		}
// 	}

// 	// Try to find Gordon by examining container processes
// 	containers, err := docker.ListRunningContainers()
// 	if err == nil {
// 		for _, container := range containers {
// 			containerID := container.ID

// 			// Check container processes for Gordon
// 			containerLogs, err := docker.GetContainerLogs(containerID)
// 			if err == nil && (strings.Contains(containerLogs, "/gordon") || strings.Contains(containerLogs, "serve")) {
// 				containerName := strings.TrimLeft(container.Names[0], "/")
// 				logger.Debug("Auto-detected Gordon container by process", "container_name", containerName)
// 				return containerName
// 			} else if err != nil {
// 				logger.Debug("Could not get container logs", "container_id", containerID, "error", err)
// 			}

// 			// Check container command for Gordon
// 			if len(container.Command) > 0 && strings.Contains(container.Command, "gordon") {
// 				containerName := strings.TrimLeft(container.Names[0], "/")
// 				logger.Debug("Auto-detected Gordon container by command", "container_name", containerName)
// 				return containerName
// 			}
// 		}
// 	} else {
// 		logger.Debug("Could not list containers when checking processes", "error", err)
// 	}

// 	// If we couldn't detect from processes, try to find a container with gordon in the name or image
// 	if err == nil {
// 		for _, container := range containers {
// 			// Check container names for "gordon"
// 			for _, name := range container.Names {
// 				if strings.Contains(strings.ToLower(name), "gordon") {
// 					containerName := strings.TrimLeft(name, "/")
// 					logger.Debug("Auto-detected Gordon container by name", "container_name", containerName)
// 					return containerName
// 				}
// 			}

// 			// Check image name for "gordon"
// 			if strings.Contains(strings.ToLower(container.Image), "gordon") {
// 				containerName := strings.TrimLeft(container.Names[0], "/")
// 				logger.Debug("Auto-detected Gordon container by image", "container_name", containerName, "image", container.Image)
// 				return containerName
// 			}
// 		}
// 	}

// 	// If all else fails, default to "gordon"
// 	logger.Debug("Could not auto-detect Gordon container, using default name", "container_name", "gordon")
// 	return "gordon"
// }

// StartHTTPSServer starts the HTTPS server
func (p *Proxy) StartHTTPSServer() error {
	logger.Info("Starting HTTPS server",
		"port", p.config.Port,
		"mode", p.config.LetsEncryptMode)

	// Verify the certificate manager is initialized (it's done in NewProxy now)
	if p.certificateManager == nil {
		// This should ideally not happen if NewProxy succeeded
		logger.Error("Certificate manager is nil in StartHTTPSServer, cannot start HTTPS")
		return fmt.Errorf("certificate manager not initialized")
	}
	logger.Info("Using new certificate management system")

	// Setup TLS configuration
	tlsConfig := &tls.Config{
		// Use the helper function that gets the GetCertificate func from the manager
		GetCertificate: p.getTLSCertificateFunc(),
		// Ensure we use TLS 1.2 or higher
		MinVersion: tls.VersionTLS12,
		// Optimize cipher suites for security and performance
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
		},
		// Use server's preferred ciphersuite
		PreferServerCipherSuites: true,
		// Enable HTTP/2
		NextProtos: []string{"h2", "http/1.1"},
		// Add server name to use when client doesn't send SNI
		ServerName: p.app.GetConfig().Http.FullDomain(),
	}

	// Removed setup for ACME challenge handler on the persistent HTTP server,
	// as it's no longer running. Lego's temporary server will handle challenges.
	// p.httpServer.Any("/.well-known/acme-challenge/*", echo.WrapHandler(p.getACMEHTTPHandler()))

	// Create a custom HTTPS server with proper TLS config
	httpsServer := &http.Server{
		Addr:      fmt.Sprintf(":%s", p.config.Port),
		Handler:   p.httpsServer,
		TLSConfig: tlsConfig,
		// Add increased timeouts to prevent hanging connections
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 30 * time.Second, // Add header timeout
	}

	// Store the HTTPS server for later use in Stop()
	p.httpsServer.Server = httpsServer

	// // Create a custom HTTP server
	// httpServer := &http.Server{
	// 	Addr:         fmt.Sprintf(":%s", p.config.HttpPort),
	// 	Handler:      p.httpServer,
	// 	ReadTimeout:  30 * time.Second,
	// 	WriteTimeout: 30 * time.Second,
	// 	IdleTimeout:  120 * time.Second,
	// }

	p.serverStarted = true

	// httpServer is no longer needed as ACME HTTP-01 uses its own temporary server
	// httpServer := &http.Server{
	// 	Addr:         fmt.Sprintf(":%s", p.config.HttpPort),
	// 	Handler:      p.httpServer,
	// 	ReadTimeout:  30 * time.Second,
	// 	WriteTimeout: 30 * time.Second,
	// 	IdleTimeout:  120 * time.Second,
	// }

	// Start HTTPS server with more error details
	go func() {
		logger.Info("Starting HTTPS server", "address", httpsServer.Addr)
		// Using empty strings for cert and key files since we're using GetCertificate
		if err := httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTPS server failed", "error", err, "address", httpsServer.Addr)

			// Print more diagnostic information
			if strings.Contains(err.Error(), "bind: address already in use") {
				logger.Error("Port 443 is already in use by another process. Check if another web server is running.",
					"solution", "Stop other services using port 443 or configure Gordon to use a different HTTPS port")
			} else if strings.Contains(err.Error(), "permission denied") {
				logger.Error("Permission denied when binding to port 443. This often happens when running without root privileges.",
					"solution", "Use setcap to allow binding to privileged ports or run in a container with proper port mapping")
			} else if strings.Contains(err.Error(), "certificate") {
				logger.Error("Certificate-related error when starting HTTPS server",
					"solution", "Check certificate files and permissions in the certificate directory")
			}
		}
	}()

	// Test admin connection after server starts
	go p.TestAdminConnectionLater()

	return nil
}

// getTLSCertificateFunc returns a function that gets certificates for TLS handshakes
func (p *Proxy) getTLSCertificateFunc() func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		logger.Debug("Getting certificate for TLS handshake", "server_name", hello.ServerName)
		return p.certificateManager.GetCertificateForTLS(hello)
	}
}
