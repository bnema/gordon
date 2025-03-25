package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/acme"
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

	// Set up middleware
	p.setupMiddleware()

	// Configure routes
	p.configureRoutes()

	// Configure HTTP server to handle Let's Encrypt HTTP-01 challenges
	// and redirect everything else to HTTPS
	p.httpServer.Any("/.well-known/acme-challenge/*", echo.WrapHandler(p.certManager.HTTPHandler(nil)))

	// Create a custom HTTPS server with proper TLS config
	httpsServer := &http.Server{
		Addr:    fmt.Sprintf(":%s", p.config.Port),
		Handler: p.httpsServer,
		TLSConfig: &tls.Config{
			GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				// Log SNI information in debug mode
				if hello.ServerName != "" {
					logger.Debug("TLS handshake with SNI",
						"server_name", hello.ServerName)
				}

				// Verify certificate manager is properly initialized
				if p.certManager == nil {
					logger.Error("TLS certificate manager is nil during handshake - certificate initialization failed",
						"server_name", hello.ServerName,
						"solution", "Check certificate configuration and logs for setup errors")

					// Use fallback certificate if we have one
					if p.fallbackCert != nil {
						logger.Warn("Using fallback self-signed certificate due to missing certificate manager",
							"server_name", hello.ServerName)
						return p.fallbackCert, nil
					}

					return nil, fmt.Errorf("certificate manager not initialized")
				}

				// Try to get the certificate from the autocert manager
				logger.Debug("Attempting to get certificate from manager",
					"server_name", hello.ServerName,
					"has_fallback", p.fallbackCert != nil)

				// First try checking if we have a valid certificate in cache
				cacheKey := "cert-" + hello.ServerName
				certCache := p.certManager.Cache
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				if certCache != nil {
					certData, err := certCache.Get(ctx, cacheKey)
					if err == nil && len(certData) > 0 {
						logger.Debug("Found certificate in cache", "domain", hello.ServerName)
						// Use directly if found in cache - skip the potentially slower autocert.Manager GetCertificate
						keyKey := "key-" + hello.ServerName
						keyData, keyErr := certCache.Get(ctx, keyKey)
						if keyErr == nil && len(keyData) > 0 {
							cert, err := tls.X509KeyPair(certData, keyData)
							if err == nil {
								logger.Debug("Successfully loaded certificate from cache",
									"domain", hello.ServerName)
								return &cert, nil
							}
							logger.Error("Failed to create X509KeyPair from cache",
								"domain", hello.ServerName,
								"error", err)
						} else {
							logger.Error("Certificate found in cache but key is missing or invalid",
								"domain", hello.ServerName,
								"key_error", keyErr)
						}
					} else if err != nil {
						logger.Debug("Certificate not found in cache during TLS handshake",
							"domain", hello.ServerName,
							"error", err)

						// Check if we have it on disk but not in cache (unlikely after preloading)
						hasOnDisk, certType := p.checkCertificateInFilesystem(hello.ServerName)
						if hasOnDisk {
							logger.Warn("Certificate found on disk but not in cache - this should not happen after preloading",
								"domain", hello.ServerName,
								"type", certType,
								"solution", "Check preloadCertificates implementation")
						}
					}
				}

				// If not in cache, use the standard GetCertificate method
				// Let's Encrypt may be contacted if no certificate exists
				cert, err := p.certManager.GetCertificate(hello)

				if err != nil {
					logger.Error("Error getting certificate from manager",
						"server_name", hello.ServerName,
						"error", err)

					// Check for rate limit errors
					if strings.Contains(err.Error(), "too many certificates") ||
						strings.Contains(err.Error(), "rateLimited") {
						logger.Error("Let's Encrypt rate limit hit during TLS handshake",
							"domain", hello.ServerName,
							"solution", "Wait for rate limit to expire or manually add certificate")
					}

					// Try manually loading certificate files directly as a fallback before using fallback cert
					if manualCert, manualErr := p.manuallyLoadCertificate(hello.ServerName); manualErr == nil {
						logger.Info("Successfully loaded certificate manually as fallback",
							"domain", hello.ServerName)
						return manualCert, nil
					} else {
						logger.Debug("Manual certificate loading also failed",
							"domain", hello.ServerName,
							"error", manualErr)
					}

					// If we can't get a certificate and we have a fallback, use it for the admin domain
					if hello.ServerName == p.app.GetConfig().Http.FullDomain() && p.fallbackCert != nil {
						logger.Debug("Using fallback certificate for admin domain",
							"domain", hello.ServerName,
							"error", err)
						return p.fallbackCert, nil
					}
				} else if cert == nil {
					logger.Error("Certificate manager returned nil certificate without error",
						"server_name", hello.ServerName)

					// Try manually loading certificate files directly as a fallback
					if manualCert, manualErr := p.manuallyLoadCertificate(hello.ServerName); manualErr == nil {
						logger.Info("Successfully loaded certificate manually when autocert returned nil",
							"domain", hello.ServerName)
						return manualCert, nil
					}

					// If we can't get a certificate and we have a fallback, use it for the admin domain
					if hello.ServerName == p.app.GetConfig().Http.FullDomain() && p.fallbackCert != nil {
						logger.Debug("Using fallback certificate for admin domain",
							"domain", hello.ServerName)
						return p.fallbackCert, nil
					}
				} else {
					logger.Debug("Successfully obtained certificate from manager",
						"server_name", hello.ServerName)
				}

				return cert, err
			},
			MinVersion: tls.VersionTLS12,
			// Add server name to use when client doesn't send SNI
			ServerName: p.app.GetConfig().Http.FullDomain(),
			// Add support for TLS-ALPN-01 challenges
			NextProtos: []string{acme.ALPNProto},
		},
		// Add increased timeouts to prevent hanging connections
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 30 * time.Second, // Add header timeout
	}

	// Store the HTTPS server for later use in Stop()
	p.httpsServer.Server = httpsServer

	// Create a custom HTTP server
	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%s", p.config.HttpPort),
		Handler:      p.httpServer,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	p.serverStarted = true

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

	// Start HTTP server
	go func() {
		logger.Info("Starting HTTP server", "address", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server failed", "error", err)
		}
	}()

	// Test admin connection after server starts
	go p.TestAdminConnectionLater()

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

	// Verify TLS settings are consistent
	if p.certManager == nil {
		logger.Error("Certificate manager is nil - TLS will not work correctly",
			"solution", "Check setupCertManager implementation")
	}

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
