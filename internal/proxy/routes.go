package proxy

import (
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/bnema/gordon/pkg/docker"
	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
)

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

// configureRoutes sets up the HTTP and HTTPS routes
func (p *Proxy) configureRoutes() {
	// Get the admin domain from the config
	adminDomain := p.app.GetConfig().Http.FullDomain()
	log.Debug("Configuring proxy routes", "admin_domain", adminDomain)

	// Handler function - will be assigned to both HTTP and HTTPS servers
	handler := echo.HandlerFunc(func(c echo.Context) error {
		host := c.Request().Host

		// Important: Get the route freshly from the map for every request
		// This ensures we always use the most up-to-date IP
		p.mu.RLock()
		route, ok := p.routes[host]
		p.mu.RUnlock()

		if !ok {
			// Check if the host is an IP address - silently handle without logging warnings
			if net.ParseIP(host) != nil {
				// For IP-based requests, just return a 404 without logging warnings
				return c.String(http.StatusNotFound, "Domain not found")
			}

			// Create list of available domains for debugging
			availableDomains := make([]string, 0, len(p.routes))
			p.mu.RLock()
			for d := range p.routes {
				availableDomains = append(availableDomains, d)
			}
			p.mu.RUnlock()

			// Log warning for non-IP hosts that aren't configured
			log.Warn("Request with unknown host",
				"requested_host", host,
				"client_ip", c.RealIP(),
				"available_domains", strings.Join(availableDomains, ", "))
			return c.String(http.StatusNotFound, "Domain not found")
		}

		// Check if the route is active
		if !route.Active {
			log.Warn("Route is not active",
				"domain", host,
				"client_ip", c.RealIP())
			return c.String(http.StatusServiceUnavailable, "Route is not active")
		}

		// Special handling for the admin domain
		if host == adminDomain {
			log.Debug("Proxying request to admin domain",
				"domain", host,
				"target", fmt.Sprintf("%s://%s:%s", route.Protocol, route.ContainerIP, route.ContainerPort),
				"path", c.Request().URL.Path)
		}

		// For container routes, verify the IP is reachable before proxying
		if host != adminDomain {
			// Test TCP connection to container before proxying
			dialer := net.Dialer{Timeout: 500 * time.Millisecond}
			// Format address properly for both IPv4 and IPv6
			target := route.ContainerIP
			if strings.Contains(target, ":") {
				// IPv6 address needs to be wrapped in brackets
				target = "[" + target + "]"
			}
			conn, err := dialer.Dial("tcp", net.JoinHostPort(target, route.ContainerPort))
			if err != nil {
				// Connection failed, check if container exists but has a new IP
				containerID := route.ContainerID
				if containerID != "" && containerID != "gordon-server" {
					// Try to get container info and check if it has a different IP
					containerInfo, err := docker.GetContainerInfo(containerID)
					if err == nil && containerInfo.NetworkSettings != nil {
						// Check if container has a different IP
						networkName := p.app.GetConfig().ContainerEngine.Network
						if networkSettings, exists := containerInfo.NetworkSettings.Networks[networkName]; exists &&
							networkSettings.IPAddress != "" && networkSettings.IPAddress != route.ContainerIP {
							// Found container with different IP, update the route and use new IP for this request
							log.Warn("Container IP mismatch detected, using live IP instead of cached value",
								"domain", host,
								"cached_ip", route.ContainerIP,
								"live_ip", networkSettings.IPAddress)

							// Update the in-memory route for this request
							route.ContainerIP = networkSettings.IPAddress

							// Update the database in background to persist the change
							go func(domain, containerID, containerIP, containerPort string) {
								// Get the domain ID
								var domainID string
								err := p.app.GetDB().QueryRow("SELECT id FROM domain WHERE name = ?", domain).Scan(&domainID)
								if err != nil {
									log.Error("Failed to find domain ID for IP update", "domain", domain, "error", err)
									return
								}

								// Update the proxy_route record
								now := time.Now().Format(time.RFC3339)
								_, err = p.app.GetDB().Exec(`
									UPDATE proxy_route SET container_ip = ?, updated_at = ? 
									WHERE domain_id = ? AND container_id = ?`,
									containerIP, now, domainID, containerID)

								if err != nil {
									log.Error("Failed to update container IP in database",
										"domain", domain,
										"containerID", containerID,
										"error", err)
								} else {
									log.Info("Updated container IP in database after live detection",
										"domain", domain,
										"containerID", containerID,
										"old_ip", route.ContainerIP,
										"new_ip", containerIP)
								}
							}(host, containerID, networkSettings.IPAddress, route.ContainerPort)
						} else {
							log.Debug("Container exists but connection still failed",
								"domain", host,
								"container_id", containerID,
								"container_ip", route.ContainerIP,
								"container_port", route.ContainerPort,
								"error", err)
						}
					}
				}
			} else {
				// Connection successful, close it
				conn.Close()
			}
		}

		// Create the target URL
		targetURL := &url.URL{
			Scheme: "http", // Always use HTTP for internal connections
		}

		// Log protocol conversion if needed
		if route.Protocol == "https" {
			log.Debug("Converting HTTPS external route to HTTP for internal connection",
				"domain", host,
				"containerID", route.ContainerID,
				"externalProtocol", "https",
				"internalProtocol", "http")
		}

		// Format host properly for both IPv4 and IPv6
		containerIP := route.ContainerIP
		if strings.Contains(containerIP, ":") {
			// IPv6 address needs to be wrapped in brackets
			containerIP = "[" + containerIP + "]"
		}
		targetURL.Host = fmt.Sprintf("%s:%s", containerIP, route.ContainerPort)

		// Create a reverse proxy
		proxy := httputil.NewSingleHostReverseProxy(targetURL)

		// Update headers to allow for SSL redirection
		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			// Use the original protocol for forwarded proto header
			forwardedProto := "http"
			if route.Protocol == "https" {
				forwardedProto = "https"
			}
			req.Header.Set("X-Forwarded-Proto", forwardedProto)
			req.Header.Set("X-Forwarded-Host", host)
			req.Header.Set("X-Forwarded-For", c.RealIP())
			req.Header.Set("X-Real-IP", c.RealIP())

			// Debug information
			log.Debug("Proxying request",
				"host", host,
				"target", targetURL.String(),
				"path", req.URL.Path,
				"clientIP", c.RealIP(),
				"originalProtocol", route.Protocol)
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

	// Add the handler to the HTTPS server
	if p.httpsServer != nil {
		p.httpsServer.Any("/*", handler)
	}

	// Also add the handler to the HTTP server for HTTP-01 challenges
	if p.httpServer != nil {
		p.httpServer.Any("/*", handler)
	}
}

// AddRoute adds a new route to the database and reloads the proxy
// The protocol parameter specifies the external protocol (http or https) for client connections.
// Note: Regardless of the external protocol, all internal connections to containers use HTTP.
// The HTTPS protocol is only used for external connections and certificates.
func (p *Proxy) AddRoute(domainName, containerID, containerIP, containerPort, protocol, path string) error {
	// Begin a transaction
	tx, err := p.app.GetDB().Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Extract the hostname from domainName if it contains a protocol
	hostname := domainName
	if strings.Contains(hostname, "://") {
		parsedURL, err := url.Parse(hostname)
		if err == nil && parsedURL.Host != "" {
			hostname = parsedURL.Host
		}
	}

	// Get the account ID - default to the first account if available
	var accountID string
	err = tx.QueryRow("SELECT id FROM account LIMIT 1").Scan(&accountID)
	if err != nil {
		if err == sql.ErrNoRows {
			log.Warn("No account found when adding route, domains will have NULL account_id")
		} else {
			log.Warn("Error fetching account for route, continuing anyway", "error", err)
		}
		// Continue with NULL account_id if no account found
	}

	// Check if the domain exists
	var domainID string
	err = tx.QueryRow("SELECT id FROM domain WHERE name = ?", hostname).Scan(&domainID)
	if err != nil {
		if err == sql.ErrNoRows {
			// Domain doesn't exist, create it
			domainID = proxyGenerateUUID()
			now := time.Now().Format(time.RFC3339)
			_, err = tx.Exec(
				"INSERT INTO domain (id, name, account_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
				domainID, hostname, accountID, now, now,
			)
			if err != nil {
				return fmt.Errorf("failed to insert domain: %w", err)
			}

			log.Debug("Added domain to database for Let's Encrypt",
				"domain", hostname)

			// Also add the subdomain.domain format to the database if this is a domain with components
			hostParts := strings.Split(hostname, ".")
			if len(hostParts) >= 3 {
				// This appears to be in format subdomain.domain.tld
				// Extract subdomain and domain parts
				subdomain := hostParts[0]
				domainPart := strings.Join(hostParts[1:], ".")

				// Check if the domain part already exists
				var domainPartID string
				err = tx.QueryRow("SELECT id FROM domain WHERE name = ?", domainPart).Scan(&domainPartID)
				if err != nil {
					if err == sql.ErrNoRows {
						// Domain part doesn't exist, create it
						domainPartID = proxyGenerateUUID()
						_, err = tx.Exec(
							"INSERT INTO domain (id, name, account_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
							domainPartID, domainPart, accountID, now, now,
						)
						if err != nil {
							log.Warn("Failed to insert domain part, continuing anyway",
								"domain_part", domainPart,
								"error", err)
						} else {
							log.Debug("Added domain part to database for Let's Encrypt",
								"domain_part", domainPart,
								"subdomain", subdomain)
						}
					}
				}
			}
		} else {
			return fmt.Errorf("failed to query domain: %w", err)
		}
	}

	// Check if a route already exists for this domain
	var existingRouteID string
	var existingContainerID string
	var existingContainerPort string
	err = tx.QueryRow(`
		SELECT id, container_id, container_port 
		FROM proxy_route 
		WHERE domain_id = ?`, domainID).Scan(&existingRouteID, &existingContainerID, &existingContainerPort)

	if err == nil {
		// Route exists, update it
		now := time.Now().Format(time.RFC3339)

		// Log more detailed information about the update
		if existingContainerID != containerID {
			log.Info("Updating proxy route for recreated container",
				"domain", hostname,
				"old_container_id", existingContainerID,
				"new_container_id", containerID)
		}

		if existingContainerPort != containerPort {
			log.Info("Container port changed for domain",
				"domain", hostname,
				"old_port", existingContainerPort,
				"new_port", containerPort)
		}

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
		routeID := proxyGenerateUUID()
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
		"domain", hostname,
		"containerIP", containerIP,
		"containerPort", containerPort,
	)

	// Request a certificate if this is an HTTPS route
	if strings.ToLower(protocol) == "https" {
		log.Info("Requesting Let's Encrypt certificate for new HTTPS route",
			"domain", hostname)
		// Run in a goroutine to avoid blocking
		go func(domain string) {
			// Set the flag to indicate we're processing a specific domain
			p.processingSpecificDomain = true
			// Try to request certificate for domain
			p.requestDomainCertificate(domain)
			// Reset the flag after we're done
			p.processingSpecificDomain = false
		}(hostname)
	}

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

// Reload reloads the routes from the database and reconfigures the proxy
func (p *Proxy) Reload() error {
	log.Info("Reloading proxy routes from database")

	// Create a new route map instead of modifying the existing one
	// This ensures a clean reload without any potential race conditions
	newRoutes := make(map[string]*ProxyRouteInfo)

	// Load routes from the database into the new map
	rows, err := p.app.GetDB().Query(`
		SELECT pr.id, d.name, pr.container_id, pr.container_ip, pr.container_port, pr.protocol, pr.path, pr.active
		FROM proxy_route pr
		JOIN domain d ON pr.domain_id = d.id
		WHERE pr.active = 1
	`)
	if err != nil {
		if err == sql.ErrNoRows {
			log.Info("No active proxy routes found during reload")
			// Even if no routes found, proceed with the swap to clear any old routes
		} else {
			return fmt.Errorf("failed to query database during reload: %w", err)
		}
	} else {
		defer rows.Close()

		// Populate the new routes map
		for rows.Next() {
			var id, domain, containerID, containerIP, containerPort, protocol, path string
			var active bool
			if err := rows.Scan(&id, &domain, &containerID, &containerIP, &containerPort, &protocol, &path, &active); err != nil {
				return fmt.Errorf("failed to scan row during reload: %w", err)
			}

			// Add the route to the new map
			newRoutes[domain] = &ProxyRouteInfo{
				Domain:        domain,
				ContainerID:   containerID,
				ContainerIP:   containerIP,
				ContainerPort: containerPort,
				Protocol:      protocol,
				Path:          path,
				Active:        active,
			}

			log.Info("Loaded route during reload",
				"domain", domain,
				"containerID", containerID,
				"containerIP", containerIP,
				"containerPort", containerPort)
		}
	}

	// Atomically swap the new routes map with the old one
	// This ensures that no requests use a partially updated map
	p.mu.Lock()
	p.routes = newRoutes
	p.mu.Unlock()

	// Rebuild route configuration
	p.configureRoutes()

	// Force recreation of the route handlers if servers are initialized
	if p.httpServer != nil {
		log.Debug("Refreshing HTTP routes")
		p.httpServer.Routes()
	}

	if p.httpsServer != nil {
		log.Debug("Refreshing HTTPS routes")
		p.httpsServer.Routes()
	}

	// Add a small delay to allow any in-flight requests to complete
	// and ensure new requests use the updated routes
	time.Sleep(100 * time.Millisecond)

	log.Info("Successfully reloaded proxy routes", "count", len(newRoutes))
	return nil
}

// ForceUpdateRouteIP updates a container's IP address both in the database and in-memory cache
// This function is useful when a container's IP has changed but the proxy is still using the old IP
func (p *Proxy) ForceUpdateRouteIP(domain string, newIP string) error {
	// First, check if the route exists
	p.mu.RLock()
	route, exists := p.routes[domain]
	p.mu.RUnlock()

	if !exists {
		return fmt.Errorf("route not found for domain: %s", domain)
	}

	// Get the container ID from the route
	containerID := route.ContainerID

	// First, update the in-memory route immediately
	p.mu.Lock()
	if r, ok := p.routes[domain]; ok {
		oldIP := r.ContainerIP
		r.ContainerIP = newIP
		log.Info("Force updated route IP in-memory",
			"domain", domain,
			"old_ip", oldIP,
			"new_ip", newIP)
	}
	p.mu.Unlock()

	// Next, update the database
	// Get the domain ID
	var domainID string
	err := p.app.GetDB().QueryRow("SELECT id FROM domain WHERE name = ?", domain).Scan(&domainID)
	if err != nil {
		return fmt.Errorf("failed to find domain ID: %w", err)
	}

	// Update the proxy_route record
	now := time.Now().Format(time.RFC3339)
	result, err := p.app.GetDB().Exec(`
		UPDATE proxy_route SET container_ip = ?, updated_at = ? 
		WHERE domain_id = ? AND container_id = ?`,
		newIP, now, domainID, containerID)

	if err != nil {
		return fmt.Errorf("failed to update database: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("no rows affected when updating route in database")
	}

	log.Info("Force updated route IP in database",
		"domain", domain,
		"container_id", containerID,
		"new_ip", newIP)

	return nil
}

// FindRoutesByContainerName finds routes associated with a specific container name
func (p *Proxy) FindRoutesByContainerName(containerName string) []string {
	// Get all containers to map IDs to names
	containers, err := docker.ListRunningContainers()
	if err != nil {
		log.Error("Failed to list containers when finding routes by container name", "error", err)
		return nil
	}

	// Create a map of container names to IDs
	containerNameToID := make(map[string]string)
	for _, c := range containers {
		for _, name := range c.Names {
			// Container names in the Docker API start with a slash
			cleanName := strings.TrimPrefix(name, "/")
			containerNameToID[cleanName] = c.ID
		}
	}

	// This is the exact container ID we're looking for
	targetContainerID := containerNameToID[containerName]
	if targetContainerID == "" {
		log.Debug("Container name not found in running containers", "container_name", containerName)
		return nil
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	// Find all domains that route to this container ID
	domains := []string{}
	for domain, route := range p.routes {
		if route.ContainerID == targetContainerID {
			domains = append(domains, domain)
		}
	}

	return domains
}

// FindRoutesByOldName tries to find routes that might have been associated with a container
// that was recreated with the same name but a new ID
func (p *Proxy) FindRoutesByOldName(containerName string) map[string]*ProxyRouteInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make(map[string]*ProxyRouteInfo)

	// Query the database to find if any route might be associated with this container name
	rows, err := p.app.GetDB().Query(`
		SELECT d.name, pr.container_id, pr.container_ip, pr.container_port, pr.protocol, pr.path
		FROM proxy_route pr
		JOIN domain d ON pr.domain_id = d.id
		WHERE pr.active = 1
	`)
	if err != nil {
		log.Error("Failed to query database for routes by old name", "error", err)
		return result
	}
	defer rows.Close()

	// Check each route to see if it might be associated with the container name
	for rows.Next() {
		var domain, containerID, containerIP, containerPort, protocol, path string
		if err := rows.Scan(&domain, &containerID, &containerIP, &containerPort, &protocol, &path); err != nil {
			log.Error("Failed to scan row", "error", err)
			continue
		}

		// Try to get the container name from this ID
		name, err := docker.GetContainerName(containerID)
		if err != nil {
			// Container might not exist anymore - this could be our candidate
			// Let's check if this route appears to be orphaned (container no longer exists)
			if !docker.ContainerExists(containerID) {
				// This is a good candidate for our container
				info, err := docker.GetContainerInfo(containerName)
				if err == nil {
					// Check if the new container has matching labels to the route
					if domainLabel, exists := info.Config.Labels["gordon.domain"]; exists && domainLabel == domain {
						result[domain] = &ProxyRouteInfo{
							Domain:        domain,
							ContainerID:   containerID, // old ID
							ContainerIP:   containerIP,
							ContainerPort: containerPort,
							Protocol:      protocol,
							Path:          path,
							Active:        true,
						}
					}
				}
			}
		} else {
			// We found an existing container - if it has the same name, this is interesting
			containerName = strings.TrimPrefix(name, "/")
			if containerName == containerName {
				// This is an exact match - shouldn't happen if we're looking for recreated containers
				// but include it anyway
				result[domain] = &ProxyRouteInfo{
					Domain:        domain,
					ContainerID:   containerID,
					ContainerIP:   containerIP,
					ContainerPort: containerPort,
					Protocol:      protocol,
					Path:          path,
					Active:        true,
				}
			}
		}
	}

	return result
}

// HandleContainerRecreation handles a container that has been recreated with the same name but a new ID
// It updates all relevant routes with the new container ID and IP
func (p *Proxy) HandleContainerRecreation(containerID, containerName, containerIP string) {
	log.Info("Handling potential container recreation",
		"container_id", containerID,
		"container_name", containerName,
		"container_ip", containerIP)

	// Get container info to check for gordon labels
	containerInfo, err := docker.GetContainerInfo(containerID)
	if err != nil {
		log.Warn("Failed to get container info", "error", err)
		return
	}

	// Check if this is a Gordon-managed container
	if _, exists := containerInfo.Config.Labels["gordon.managed"]; !exists {
		log.Debug("Ignoring non-Gordon container", "container_name", containerName)
		return
	}

	// First approach: check if there are any domains that should point to this container based on labels
	if domainLabel, exists := containerInfo.Config.Labels["gordon.domain"]; exists && domainLabel != "" {
		// Check if there's already a route for this domain
		p.mu.RLock()
		route, domainExists := p.routes[domainLabel]
		p.mu.RUnlock()

		if domainExists {
			// Route exists - check if it's for a different container ID
			if route.ContainerID != containerID {
				log.Info("Container was recreated with the same domain label",
					"domain", domainLabel,
					"old_container_id", route.ContainerID,
					"new_container_id", containerID)

				// Get the port to use for the proxy from the container labels
				containerPort := "80" // Default
				if portLabel, exists := containerInfo.Config.Labels["gordon.proxy.port"]; exists && portLabel != "" {
					containerPort = portLabel
				}

				// Get the protocol to use
				protocol := "http" // Default
				if sslLabel, exists := containerInfo.Config.Labels["gordon.proxy.ssl"]; exists &&
					(sslLabel == "true" || sslLabel == "1" || sslLabel == "yes") {
					protocol = "https"
				}

				// Update the route with the new container ID and IP
				err = p.AddRoute(domainLabel, containerID, containerIP, containerPort, protocol, route.Path)
				if err != nil {
					log.Error("Failed to update route for recreated container", "error", err)
				} else {
					log.Info("Updated route for recreated container",
						"domain", domainLabel,
						"container_id", containerID,
						"container_ip", containerIP)
				}
			} else {
				// Same container ID but maybe IP changed
				if route.ContainerIP != containerIP {
					log.Info("Container IP changed",
						"domain", domainLabel,
						"container_id", containerID,
						"old_ip", route.ContainerIP,
						"new_ip", containerIP)

					// Update the IP
					err = p.ForceUpdateRouteIP(domainLabel, containerIP)
					if err != nil {
						log.Error("Failed to update IP for container", "error", err)
					}
				}
			}
		} else {
			// No existing route for this domain - might be a new container
			log.Info("New container with domain label",
				"domain", domainLabel,
				"container_id", containerID)

			// Get the port to use for the proxy from the container labels
			containerPort := "80" // Default
			if portLabel, exists := containerInfo.Config.Labels["gordon.proxy.port"]; exists && portLabel != "" {
				containerPort = portLabel
			}

			// Get the protocol to use
			protocol := "http" // Default
			if sslLabel, exists := containerInfo.Config.Labels["gordon.proxy.ssl"]; exists &&
				(sslLabel == "true" || sslLabel == "1" || sslLabel == "yes") {
				protocol = "https"
			}

			// Add a new route
			err = p.AddRoute(domainLabel, containerID, containerIP, containerPort, protocol, "/")
			if err != nil {
				log.Error("Failed to add route for new container", "error", err)
			}
		}
	}

	// Second approach: check for routes with name-based matching that might need updating
	// This handles the case where the container name is used instead of explicit domain labels
	if domainCheck := p.FindRoutesByOldName(containerName); len(domainCheck) > 0 {
		log.Info("Found potential routes for recreated container by name matching",
			"container_name", containerName,
			"domains_count", len(domainCheck))

		for domain, oldRoute := range domainCheck {
			log.Info("Updating route for recreated container by name matching",
				"domain", domain,
				"old_container_id", oldRoute.ContainerID,
				"new_container_id", containerID)

			// Get the port to use for the proxy from the container labels
			containerPort := oldRoute.ContainerPort // Use the existing port by default
			if portLabel, exists := containerInfo.Config.Labels["gordon.proxy.port"]; exists && portLabel != "" {
				// But prefer the container label if present
				containerPort = portLabel
			}

			// Get the protocol to use
			protocol := oldRoute.Protocol // Use existing protocol by default
			if sslLabel, exists := containerInfo.Config.Labels["gordon.proxy.ssl"]; exists {
				// But prefer the container label if present
				if sslLabel == "true" || sslLabel == "1" || sslLabel == "yes" {
					protocol = "https"
				} else {
					protocol = "http"
				}
			}

			// Update the route
			err = p.AddRoute(domain, containerID, containerIP, containerPort, protocol, oldRoute.Path)
			if err != nil {
				log.Error("Failed to update route for recreated container", "error", err)
			} else {
				log.Info("Updated route for recreated container",
					"domain", domain,
					"container_id", containerID,
					"container_ip", containerIP)
			}
		}
	}
}

// StartContainerEventListener starts a goroutine that listens for container events
// and handles container recreation
func (p *Proxy) StartContainerEventListener() error {
	// Get the network name from app config
	networkName := p.app.GetConfig().ContainerEngine.Network
	if networkName == "" {
		return fmt.Errorf("container network name is not configured")
	}

	log.Info("Starting container event listener", "network", networkName)

	// Create a callback function to handle container events
	containerEventCallback := func(containerID, containerName, containerIP string) {
		p.HandleContainerRecreation(containerID, containerName, containerIP)
	}

	// Start the container event listener
	return docker.ListenForContainerEvents(networkName, containerEventCallback)
}

// StartPeriodicIPVerification starts a goroutine that periodically checks container IPs
// and updates the routes if they have changed. This helps ensure routes always have
// the correct IP address, even if the event listener missed something.
func (p *Proxy) StartPeriodicIPVerification(interval time.Duration) {
	log.Info("Starting periodic IP verification", "interval", interval)

	// Run in a background goroutine
	go func() {
		// Use a ticker for periodic execution
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				log.Debug("Running periodic IP verification")
				p.verifyAllContainerIPs()

				// Also check for any containers that should have routes but don't
				p.DiscoverMissingRoutes()

				// Log current active containers and routes for monitoring
				if log.GetLevel() <= log.DebugLevel {
					// Only log the container list in debug mode during periodic checks
					// to avoid overwhelming logs in production
					p.LogActiveContainersAndRoutes()
				}
			}
		}
	}()
}

// verifyAllContainerIPs checks all routes and verifies their container IPs
func (p *Proxy) verifyAllContainerIPs() {
	// Get the network name from app config
	networkName := p.app.GetConfig().ContainerEngine.Network
	if networkName == "" {
		log.Error("Cannot verify container IPs, network name is not configured")
		return
	}

	// Get network info
	networkInfo, err := docker.GetNetworkInfo(networkName)
	if err != nil {
		log.Error("Failed to get network info", "error", err)
		return
	}

	// Get a copy of the routes
	p.mu.RLock()
	routes := make(map[string]*ProxyRouteInfo, len(p.routes))
	for k, v := range p.routes {
		routes[k] = v
	}
	p.mu.RUnlock()

	// Track orphaned routes to mark as inactive
	orphanedRoutes := []string{}

	// Check each route
	for domain, route := range routes {
		// Check if the container exists in the network
		containerEndpoint, exists := networkInfo.Containers[route.ContainerID]
		if !exists {
			// Container not found in the network
			// Could be stopped or removed

			// Check if container still exists
			if docker.ContainerExists(route.ContainerID) {
				// Container exists but not in our network
				log.Warn("Container exists but not in Gordon network",
					"domain", domain,
					"container_id", route.ContainerID)
			} else {
				// Container no longer exists
				log.Warn("Container no longer exists but route is active",
					"domain", domain,
					"container_id", route.ContainerID)
				orphanedRoutes = append(orphanedRoutes, domain)
			}
			continue
		}

		// Extract container IP from network info
		containerIP := containerEndpoint.IPv4Address
		if containerIP == "" {
			// Try IPv6
			containerIP = containerEndpoint.IPv6Address
		}

		// Extract just the IP part without subnet
		if idx := strings.Index(containerIP, "/"); idx > 0 {
			containerIP = containerIP[:idx]
		}

		// Compare with the route's IP
		if containerIP != route.ContainerIP {
			log.Info("Container IP has changed, updating route",
				"domain", domain,
				"container_id", route.ContainerID,
				"old_ip", route.ContainerIP,
				"new_ip", containerIP)

			// Update the route IP
			err := p.ForceUpdateRouteIP(domain, containerIP)
			if err != nil {
				log.Error("Failed to update route IP", "error", err)
			} else {
				log.Debug("Successfully updated route IP",
					"domain", domain,
					"new_ip", containerIP)
			}
		}
	}

	// Mark orphaned routes as inactive
	if len(orphanedRoutes) > 0 {
		log.Info("Marking orphaned routes as inactive", "count", len(orphanedRoutes))
		p.markRoutesInactive(orphanedRoutes)
	}

	log.Debug("Completed IP verification", "routes_checked", len(routes), "orphaned_routes", len(orphanedRoutes))
}

// markRoutesInactive marks the specified routes as inactive in the database and memory
func (p *Proxy) markRoutesInactive(domains []string) {
	if len(domains) == 0 {
		return
	}

	// Begin a transaction
	tx, err := p.app.GetDB().Begin()
	if err != nil {
		log.Error("Failed to begin transaction for marking routes inactive", "error", err)
		return
	}
	defer tx.Rollback()

	// Update each route in the database
	for _, domain := range domains {
		// First, get the domain ID
		var domainID string
		err := tx.QueryRow("SELECT id FROM domain WHERE name = ?", domain).Scan(&domainID)
		if err != nil {
			log.Error("Failed to get domain ID for inactive route", "domain", domain, "error", err)
			continue
		}

		// Update the route to mark it as inactive
		now := time.Now().Format(time.RFC3339)
		_, err = tx.Exec("UPDATE proxy_route SET active = 0, updated_at = ? WHERE domain_id = ?", now, domainID)
		if err != nil {
			log.Error("Failed to mark route as inactive in database", "domain", domain, "error", err)
			continue
		}

		// Also update the in-memory route
		p.mu.Lock()
		if route, exists := p.routes[domain]; exists {
			route.Active = false
			log.Info("Marked route as inactive", "domain", domain)
		}
		p.mu.Unlock()
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		log.Error("Failed to commit transaction for marking routes inactive", "error", err)
		return
	}

	log.Info("Successfully marked orphaned routes as inactive", "count", len(domains))
}

// DiscoverMissingRoutes finds Gordon-managed containers that don't have routes yet
// and adds them to the routing table
func (p *Proxy) DiscoverMissingRoutes() {
	log.Info("Discovering containers that might need routes")

	// Get the network name from app config
	networkName := p.app.GetConfig().ContainerEngine.Network
	if networkName == "" {
		log.Error("Cannot discover missing routes, network name is not configured")
		return
	}

	// Get all containers in the network
	networkInfo, err := docker.GetNetworkInfo(networkName)
	if err != nil {
		log.Error("Failed to get network info", "error", err)
		return
	}

	// Get a copy of the current routes for domain lookups
	p.mu.RLock()
	routes := make(map[string]*ProxyRouteInfo, len(p.routes))
	for k, v := range p.routes {
		routes[k] = v
	}
	p.mu.RUnlock()

	// Create a map of container IDs that already have routes
	routedContainerIDs := make(map[string]bool)
	for _, route := range routes {
		routedContainerIDs[route.ContainerID] = true
	}

	// Check each container in the network
	for containerID, containerEndpoint := range networkInfo.Containers {
		// Skip if this container already has a route
		if routedContainerIDs[containerID] {
			continue
		}

		// Get container info to check for Gordon labels
		containerInfo, err := docker.GetContainerInfo(containerID)
		if err != nil {
			log.Debug("Failed to get container info",
				"container_id", containerID,
				"error", err)
			continue
		}

		// Check if this is a Gordon-managed container
		if _, exists := containerInfo.Config.Labels["gordon.managed"]; !exists {
			continue
		}

		// Check if it has a domain label
		domainLabel, hasDomain := containerInfo.Config.Labels["gordon.domain"]
		if !hasDomain || domainLabel == "" {
			log.Debug("Gordon container without domain label",
				"container_id", containerID,
				"container_name", strings.TrimPrefix(containerInfo.Name, "/"))
			continue
		}

		// Check if this domain already has a route (different container)
		if _, exists := routes[domainLabel]; exists {
			log.Warn("Domain already has a route with a different container",
				"domain", domainLabel,
				"container_id", containerID,
				"existing_container_id", routes[domainLabel].ContainerID)
			continue
		}

		// Extract container IP and port for the new route
		containerIP := containerEndpoint.IPv4Address
		if containerIP == "" {
			containerIP = containerEndpoint.IPv6Address
		}

		// Extract just the IP part without subnet
		if idx := strings.Index(containerIP, "/"); idx > 0 {
			containerIP = containerIP[:idx]
		}

		// Get the port to use for the proxy from the container labels
		containerPort := "80" // Default
		if portLabel, exists := containerInfo.Config.Labels["gordon.proxy.port"]; exists && portLabel != "" {
			containerPort = portLabel
		}

		// Get the protocol to use
		protocol := "http" // Default
		if sslLabel, exists := containerInfo.Config.Labels["gordon.proxy.ssl"]; exists &&
			(sslLabel == "true" || sslLabel == "1" || sslLabel == "yes") {
			protocol = "https"
		}

		// Add a new route for this container
		log.Info("Adding missing route for Gordon container",
			"domain", domainLabel,
			"container_id", containerID,
			"container_ip", containerIP,
			"container_port", containerPort)

		err = p.AddRoute(domainLabel, containerID, containerIP, containerPort, protocol, "/")
		if err != nil {
			log.Error("Failed to add missing route", "error", err)
		}
	}
}

// LogActiveContainersAndRoutes prints a summary of all active containers and their routes
// to the logs for debugging and monitoring purposes
func (p *Proxy) LogActiveContainersAndRoutes() {
	// Get the network name from app config
	networkName := p.app.GetConfig().ContainerEngine.Network
	if networkName == "" {
		log.Error("Cannot list containers, network name is not configured")
		return
	}

	// Get all containers in the network
	networkInfo, err := docker.GetNetworkInfo(networkName)
	if err != nil {
		log.Error("Failed to get network info for logging active containers", "error", err)
		return
	}

	// Get all active routes
	p.mu.RLock()
	routes := make(map[string]*ProxyRouteInfo, len(p.routes))
	for k, v := range p.routes {
		if v.Active {
			routes[k] = v
		}
	}
	p.mu.RUnlock()

	// Map container IDs to names for more readable output
	containerNames := make(map[string]string)
	for containerID := range networkInfo.Containers {
		name, err := docker.GetContainerName(containerID)
		if err == nil {
			containerNames[containerID] = strings.TrimPrefix(name, "/")
		} else {
			containerNames[containerID] = containerID[:12] + "..." // Shortened ID if name not available
		}
	}

	// Format and log the active containers and routes
	log.Info("---- Listing active containers and routes ----")

	// Create a nice table-like format for the logs
	routeCount := len(routes)
	if routeCount == 0 {
		log.Info("No active routes found")
	} else {
		// Log header
		log.Info(fmt.Sprintf("Found %d active routes", routeCount))

		// Group routes by container for better readability
		containerRoutes := make(map[string][]string)
		for domain, route := range routes {
			containerRoutes[route.ContainerID] = append(containerRoutes[route.ContainerID], domain)
		}

		// Display each container and its routes
		for containerID, domains := range containerRoutes {
			containerInfo, err := docker.GetContainerInfo(containerID)
			if err != nil {
				log.Debug("Failed to get container info", "container_id", containerID)
				continue
			}

			containerName := containerNames[containerID]
			if containerName == "" {
				containerName = containerID[:12] + "..." // Shortened ID if name not available
			}

			// Get container IP from network info
			containerIP := ""
			if endpoint, exists := networkInfo.Containers[containerID]; exists {
				containerIP = endpoint.IPv4Address
				if containerIP == "" {
					containerIP = endpoint.IPv6Address
				}

				// Extract just the IP part without subnet
				if idx := strings.Index(containerIP, "/"); idx > 0 {
					containerIP = containerIP[:idx]
				}
			}

			// Log container info
			log.Info(fmt.Sprintf("Container: %s (%s)", containerName, containerID[:12]),
				"ip", containerIP,
				"image", containerInfo.Config.Image)

			// Log each domain for this container
			for _, domain := range domains {
				route := routes[domain]
				log.Info(fmt.Sprintf("  ├─ Domain: %s", domain),
					"protocol", route.Protocol,
					"port", route.ContainerPort)
			}
		}
	}

	log.Info("---- End of active containers and routes list ----")
}
