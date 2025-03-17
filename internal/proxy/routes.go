package proxy

import (
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/logger"
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

// loadRoutes initializes the proxy by detecting the Gordon container ID
func (p *Proxy) loadRoutes() error {
	logger.Debug("Initializing proxy")

	// Now check if we have a Gordon container ID stored for identity purposes
	p.gordonContainerID = p.detectGordonContainer()
	if p.gordonContainerID != "" {
		logger.Info("Loaded Gordon container ID during initialization", "container_id", p.gordonContainerID)
	}

	// Log the number of active routes
	routes := p.GetRoutes()
	logger.Info("Active proxy routes", "count", len(routes))
	return nil
}

// configureRoutes sets up the HTTP and HTTPS routes
func (p *Proxy) configureRoutes() {
	// Get the admin domain from the config
	adminDomain := p.app.GetConfig().Http.FullDomain()
	logger.Debug("Configuring proxy routes", "admin_domain", adminDomain)

	// Check if admin domain route exists in the database
	adminRoute, err := p.GetRouteByDomainName(adminDomain)
	if err != nil {
		logger.Error("Error checking admin domain route", "error", err)
	}

	// If admin route doesn't exist, create it
	if adminRoute == nil {
		logger.Warn("Admin domain route is missing, attempting to recreate it")

		// Auto-detect the Gordon container name
		containerName := p.detectGordonContainer()

		// In a container environment, we need to use the host container IP
		// instead of 127.0.0.1 because each container has its own localhost
		containerIP := "localhost" // Default to localhost for most reliable connectivity

		// Fall back options for container IP
		if os.Getenv("GORDON_ADMIN_HOST") != "" {
			// Allow explicit configuration via env var
			containerIP = os.Getenv("GORDON_ADMIN_HOST")
		} else if os.Getenv("HOSTNAME") != "" {
			// Use container's own hostname as they're on the same network
			containerIP = os.Getenv("HOSTNAME")
		}

		// Add the admin route to the database
		err := p.AddRoute(
			adminDomain,
			containerName,
			containerIP,
			p.app.GetConfig().Http.Port,
			"http", // Gordon server uses HTTP internally
			"/",
		)
		if err != nil {
			logger.Error("Failed to create admin domain route", "error", err)
		} else {
			logger.Info("Recreated admin domain route",
				"domain", adminDomain,
				"target", fmt.Sprintf("http://%s:%s", containerIP, p.app.GetConfig().Http.Port))
		}
	} else {
		logger.Debug("Admin domain route exists", "domain", adminDomain)
	}

	// Handler function - will be assigned to both HTTP and HTTPS servers
	handler := echo.HandlerFunc(func(c echo.Context) error {
		host := c.Request().Host

		// Strip port if present in the host
		if hostParts := strings.Split(host, ":"); len(hostParts) > 1 {
			host = hostParts[0]
		}

		// Normalize host (trim trailing dot, convert to lowercase)
		host = strings.TrimSuffix(strings.ToLower(host), ".")

		// Check if this is the admin domain
		adminDomain := p.app.GetConfig().Http.FullDomain()
		if strings.EqualFold(host, adminDomain) {
			// Always prioritize admin domain
			adminRoute, err := p.GetRouteByDomainName(adminDomain)
			if err != nil {
				logger.Error("Error getting admin route", "error", err)
			}

			if adminRoute != nil {
				return p.proxyRequest(c, adminRoute)
			}
		}

		// Get the route from the database for every request
		// This ensures we always use the most up-to-date IP
		route, err := p.GetRouteByDomainName(host)
		if err != nil {
			logger.Error("Error getting route", "error", err, "host", host)
		}

		if route == nil {
			// Check if the host is an IP address - silently handle without logging warnings
			if net.ParseIP(host) != nil {
				// For IP-based requests, just return a 404 without logging warnings
				return c.String(http.StatusNotFound, "Domain not found")
			}

			// Create list of available domains for debugging
			routes := p.GetRoutes()
			availableDomains := make([]string, 0, len(routes))
			for d := range routes {
				availableDomains = append(availableDomains, d)
			}
			logger.Warn("Request with unknown host",
				"requested_host", host,
				"client_ip", c.RealIP(),
				"available_domains", strings.Join(availableDomains, ", "))
			return c.String(http.StatusNotFound, "Domain not found")
		}

		// Check if the route is active
		if !route.Active {
			logger.Warn("Route is not active",
				"domain", host,
				"client_ip", c.RealIP())
			return c.String(http.StatusServiceUnavailable, "Route is not active")
		}
		return p.proxyRequest(c, route)
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
	// Normalize domain name (lowercase, no trailing dot)
	domainName = strings.TrimSuffix(strings.ToLower(domainName), ".")

	logger.Debug("Adding proxy route",
		"domain", domainName,
		"container_id", containerID,
		"container_ip", containerIP,
		"container_port", containerPort,
		"protocol", protocol,
		"path", path)

	// Begin a transaction with retry
	tx, err := p.dbBeginWithRetry()
	if err != nil {
		logger.Error("Failed to begin transaction when adding route",
			"domain", domainName,
			"error", err)
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
	err = tx.QueryRow(p.queries.GetFirstAccount).Scan(&accountID)
	if err != nil {
		if err == sql.ErrNoRows {
			logger.Warn("No account found when adding route, domains will have NULL account_id")
		} else {
			logger.Warn("Error fetching account for route, continuing anyway", "error", err)
		}
		// Continue with NULL account_id if no account found
	}

	// Check if the domain exists
	var domainID string
	err = tx.QueryRow(p.queries.GetDomainByName, hostname).Scan(&domainID)
	if err != nil {
		if err == sql.ErrNoRows {
			// Domain doesn't exist, create it
			domainID = proxyGenerateUUID()
			now := time.Now().Format(time.RFC3339)
			_, err = txExecWithRetry(tx,
				p.queries.InsertDomain,
				domainID, hostname, accountID, now, now,
			)
			if err != nil {
				logger.Error("Failed to insert domain",
					"domain", hostname,
					"error", err)
				return fmt.Errorf("failed to insert domain: %w", err)
			}

			logger.Debug("Added domain to database for Let's Encrypt",
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
						_, err = txExecWithRetry(tx,
							p.queries.InsertDomain,
							domainPartID, domainPart, accountID, now, now,
						)
						if err != nil {
							logger.Warn("Failed to insert domain part, continuing anyway",
								"domain_part", domainPart,
								"error", err)
						} else {
							logger.Debug("Added domain part to database for Let's Encrypt",
								"domain_part", domainPart,
								"subdomain", subdomain)
						}
					}
				}
			}
		} else {
			logger.Error("Failed to query domain",
				"domain", hostname,
				"error", err)
			return fmt.Errorf("failed to query domain: %w", err)
		}
	}

	// Check if a route already exists for this domain
	var existingRouteID string
	var existingContainerID string
	var existingContainerPort string
	err = tx.QueryRow(p.queries.GetRouteByDomain, domainID).Scan(&existingRouteID, &existingContainerID, &existingContainerPort)

	if err == nil {
		// Route exists, update it
		now := time.Now().Format(time.RFC3339)

		// Log more detailed information about the update
		if existingContainerID != containerID {
			logger.Info("Updating proxy route for recreated container",
				"domain", hostname,
				"old_container_id", existingContainerID,
				"new_container_id", containerID)
		}

		if existingContainerPort != containerPort {
			logger.Info("Container port changed for domain",
				"domain", hostname,
				"old_port", existingContainerPort,
				"new_port", containerPort)
		}

		_, err = txExecWithRetry(tx,
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
			logger.Error("Failed to update route",
				"domain", hostname,
				"error", err)
			return fmt.Errorf("failed to update route: %w", err)
		}
	} else if err == sql.ErrNoRows {
		// Route doesn't exist, create it
		routeID := proxyGenerateUUID()
		now := time.Now().Format(time.RFC3339)
		_, err = txExecWithRetry(tx,
			p.queries.InsertRoute,
			routeID, domainID, containerID, containerIP, containerPort,
			protocol, path, true, now, now,
		)
		if err != nil {
			logger.Error("Failed to insert route",
				"domain", hostname,
				"error", err)
			return fmt.Errorf("failed to insert route: %w", err)
		}
	} else {
		logger.Error("Failed to query route",
			"domain", hostname,
			"error", err)
		return fmt.Errorf("failed to query route: %w", err)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		logger.Error("Failed to commit transaction",
			"domain", hostname,
			"error", err)
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Reload the routes in the background to avoid blocking
	go func() {
		if err := p.Reload(); err != nil {
			logger.Error("Failed to reload routes after adding route",
				"domain", hostname,
				"error", err)
		}
	}()

	return nil
}

// detectGordonContainer attempts to find the Gordon container automatically
func (p *Proxy) detectGordonContainer() string {
	logger.Debug("Starting Gordon container detection")

	// If we have our container ID stored from initialization, use that first
	if p.gordonContainerID != "" {
		logger.Debug("Using stored Gordon container ID for identification", "container_id", p.gordonContainerID)

		// Get the container name from the ID
		containers, err := docker.ListRunningContainers()
		if err == nil {
			for _, container := range containers {
				if container.ID == p.gordonContainerID {
					containerName := strings.TrimLeft(container.Names[0], "/")
					logger.Info("Gordon identity established via exact ID match",
						"container_id", container.ID,
						"container_name", containerName)
					return containerName
				}
			}
			// If the container ID is no longer found, log it but continue with other detection methods
			logger.Warn("Stored Gordon container ID no longer found in running containers", "container_id", p.gordonContainerID)
			p.gordonContainerID = "" // Reset since it's invalid
		}
	}

	// Check if we're running inside a container
	if docker.IsRunningInContainer() {
		logger.Debug("Detected we're running in a container")

		// Get our hostname which should be the container ID in Docker/Podman
		hostname := os.Getenv("HOSTNAME")

		if hostname != "" {
			// Check if there's a container with this hostname
			containers, err := docker.ListRunningContainers()
			if err == nil {
				// First try exact match (Podman often uses full ID as hostname)
				for _, container := range containers {
					if hostname == container.ID {
						containerName := strings.TrimLeft(container.Names[0], "/")
						logger.Info("Gordon identity established via exact ID match",
							"container_id", container.ID,
							"container_name", containerName)
						p.gordonContainerID = container.ID
						return containerName
					}
				}

				// Then try prefix match (Docker often uses ID prefix as hostname)
				for _, container := range containers {
					if strings.HasPrefix(container.ID, hostname) || strings.HasPrefix(hostname, container.ID) {
						containerName := strings.TrimLeft(container.Names[0], "/")
						logger.Info("Gordon identity established via ID prefix match",
							"container_id", container.ID,
							"container_name", containerName,
							"hostname", hostname)
						p.gordonContainerID = container.ID
						return containerName
					}
				}
			} else {
				logger.Debug("Could not list containers when checking hostname", "error", err)
			}
		}
	}

	// Look for containers with the name "gordon" first (Docker Compose default)
	containers, err := docker.ListRunningContainers()
	if err == nil {
		for _, container := range containers {
			containerName := strings.TrimLeft(container.Names[0], "/")
			if containerName == "gordon" {
				logger.Info("Found Gordon container with exact name 'gordon'", "container_id", container.ID)
				p.gordonContainerID = container.ID
				return containerName
			}
		}

		// If we still haven't found it, look for containers with 'gordon' in the name
		for _, container := range containers {
			containerName := strings.TrimLeft(container.Names[0], "/")
			if strings.Contains(strings.ToLower(containerName), "gordon") {
				logger.Info("Found Gordon container with partial name match",
					"container_name", containerName,
					"container_id", container.ID)
				p.gordonContainerID = container.ID
				return containerName
			}
		}
	}

	// Fallback to a default name
	logger.Warn("Could not auto-detect Gordon container, using fallback name", "container_name", "gordon")
	return "gordon"
}

// ForceUpdateRouteIP updates a container's IP address both in the database and in-memory cache
// This function is useful when a container's IP has changed but the proxy is still using the old IP
func (p *Proxy) ForceUpdateRouteIP(domain string, newIP string) error {
	route, err := p.GetRouteByDomainName(domain)
	if err != nil {
		return fmt.Errorf("error getting route for domain %s: %w", domain, err)
	}

	if route == nil {
		logger.Warn("Route not found for domain when updating IP", "domain", domain)
		return fmt.Errorf("route not found for domain: %s", domain)
	}

	// Get the container ID from the route
	containerID := route.ContainerID

	// Add additional debug to track the IP update
	logger.Debug("Attempting to force update route IP",
		"domain", domain,
		"container_id", containerID,
		"old_ip", route.ContainerIP,
		"new_ip", newIP)

	// Double-check that the container exists and is running
	containerInfo, err := docker.GetContainerInfo(containerID)
	if err != nil {
		logger.Warn("Container not found when updating route IP, might have been recreated",
			"domain", domain,
			"container_id", containerID,
			"error", err)
		// Continue with the update anyway, as the container ID might still be valid
	} else {
		logger.Debug("Container exists when updating route IP",
			"domain", domain,
			"container_id", containerID,
			"container_state", containerInfo.State.Status)

		// Verify the new IP matches what's in the container
		networkName := p.app.GetConfig().ContainerEngine.Network
		if containerInfo.NetworkSettings != nil && containerInfo.NetworkSettings.Networks != nil {
			if networkSettings, exists := containerInfo.NetworkSettings.Networks[networkName]; exists &&
				networkSettings.IPAddress != "" && networkSettings.IPAddress != newIP {
				logger.Warn("New IP doesn't match container network IP, using container's actual IP",
					"domain", domain,
					"provided_ip", newIP,
					"container_actual_ip", networkSettings.IPAddress)
				newIP = networkSettings.IPAddress
			}
		}
	}

	// Update the route in the database
	// Get the domain ID with retry
	var domainID string
	rows, err := p.app.GetDB().Query(p.queries.GetDomainByName, domain)
	if err != nil {
		logger.Error("Failed to query domain for IP update", "domain", domain, "error", err)
		return fmt.Errorf("failed to query domain: %w", err)
	}

	if !rows.Next() {
		rows.Close()
		logger.Error("Domain not found in database when updating IP", "domain", domain)
		return fmt.Errorf("domain not found: %s", domain)
	}

	if err := rows.Scan(&domainID); err != nil {
		rows.Close()
		logger.Error("Failed to scan domain ID for IP update", "domain", domain, "error", err)
		return fmt.Errorf("failed to scan domain ID: %w", err)
	}
	rows.Close()

	// Update the proxy_route record with retry and ensure it's marked as active
	now := time.Now().Format(time.RFC3339)
	result, err := p.app.GetDB().Exec(p.queries.UpdateRouteIP, newIP, now, domainID, containerID)

	if err != nil {
		logger.Error("Failed to update IP in database", "domain", domain, "error", err)
		return fmt.Errorf("failed to update database: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		logger.Warn("No rows affected when updating route IP in database", "domain", domain)
		return fmt.Errorf("no rows affected when updating route in database")
	}

	logger.Info("Force updated route IP in database",
		"domain", domain,
		"container_id", containerID,
		"new_ip", newIP)

	return nil
}

// GetRoutes returns all active routes from the database
func (p *Proxy) GetRoutes() map[string]*ProxyRouteInfo {
	routes := make(map[string]*ProxyRouteInfo)

	// Query the database for all active routes
	rows, err := p.dbQueryWithRetry(p.queries.GetActiveRoutes)
	if err != nil {
		logger.Error("Failed to query database for active routes", "error", err)
		return routes
	}
	defer rows.Close()

	// Populate the routes map
	for rows.Next() {
		var id, domain, containerID, containerIP, containerPort, protocol, path string
		var active bool
		if err := rows.Scan(&id, &domain, &containerID, &containerIP, &containerPort, &protocol, &path, &active); err != nil {
			logger.Error("Failed to scan row", "error", err)
			continue
		}

		// Normalize domain (lowercase, no trailing dot)
		domain = strings.TrimSuffix(strings.ToLower(domain), ".")

		// Add the route to the map
		routes[domain] = &ProxyRouteInfo{
			Domain:        domain,
			ContainerID:   containerID,
			ContainerIP:   containerIP,
			ContainerPort: containerPort,
			Protocol:      protocol,
			Path:          path,
			Active:        active,
		}
	}

	return routes
}

// Reload reloads the routes from the database and reconfigures the proxy
func (p *Proxy) Reload() error {
	logger.Info("Reloading proxy routes from database")

	// Get admin domain - this should be the full subdomain.domain format
	adminDomain := p.app.GetConfig().Http.FullDomain()
	// Normalize the admin domain (lowercase, no trailing dot)
	adminDomain = strings.TrimSuffix(strings.ToLower(adminDomain), ".")

	// Get all routes from the database
	routes := make(map[string]*ProxyRouteInfo)

	// Query the database for all routes
	rows, err := p.dbQueryWithRetry(p.queries.GetAllRoutes)
	if err != nil {
		logger.Error("Failed to query database for routes", "error", err)
		return err
	}
	defer rows.Close()

	// Populate the routes map
	for rows.Next() {
		var id, domain, containerID, containerIP, containerPort, protocol, path string
		var active bool
		if err := rows.Scan(&id, &domain, &containerID, &containerIP, &containerPort, &protocol, &path, &active); err != nil {
			logger.Error("Failed to scan row", "error", err)
			continue
		}

		routes[domain] = &ProxyRouteInfo{
			Domain:        domain,
			ContainerID:   containerID,
			ContainerIP:   containerIP,
			ContainerPort: containerPort,
			Protocol:      protocol,
			Path:          path,
			Active:        active,
		}
	}

	// Create a map of container IDs that already have routes
	containerGroups := make(map[string][]string)
	containerInfo := make(map[string]struct {
		ContainerIP string
		ContainerID string
		Protocols   []string
		Ports       []string
	})

	// Get active routes from database and count them
	var activeRoutes int
	rows, err = p.dbQueryWithRetry(p.queries.GetActiveRoutes)
	if err != nil {
		logger.Error("Failed to query database for active routes", "error", err)
		return err
	}
	defer rows.Close()

	// Process all routes from the database
	for rows.Next() {
		var id, domain, containerID, containerIP, containerPort, protocol, path string
		var active bool
		if err := rows.Scan(&id, &domain, &containerID, &containerIP, &containerPort, &protocol, &path, &active); err != nil {
			logger.Error("Failed to scan row", "error", err)
			continue
		}

		// Increment active routes counter
		activeRoutes++

		// Group by container ID
		containerGroups[containerID] = append(containerGroups[containerID], domain)

		// Store container info
		info, exists := containerInfo[containerID]
		if !exists {
			info = struct {
				ContainerIP string
				ContainerID string
				Protocols   []string
				Ports       []string
			}{
				ContainerIP: containerIP,
				ContainerID: containerID,
				Protocols:   []string{protocol},
				Ports:       []string{containerPort},
			}
		} else {
			// Add protocol if not already in the list
			protocolExists := false
			for _, p := range info.Protocols {
				if p == protocol {
					protocolExists = true
					break
				}
			}
			if !protocolExists {
				info.Protocols = append(info.Protocols, protocol)
			}

			// Add port if not already in the list
			portExists := false
			for _, p := range info.Ports {
				if p == containerPort {
					portExists = true
					break
				}
			}
			if !portExists {
				info.Ports = append(info.Ports, containerPort)
			}
		}

		containerInfo[containerID] = info
	}

	// Log admin domain status and active routes count
	if route, exists := routes[adminDomain]; !exists || !route.Active {
		logger.Warn("Admin domain is missing from routes", "domain", adminDomain)
	}
	logger.Info(fmt.Sprintf("Found %d active routes", activeRoutes))

	// Get container names and images
	containerNames := make(map[string]string)
	containerImages := make(map[string]string)

	for containerID := range containerGroups {
		containerInfo, err := docker.GetContainerInfo(containerID)
		if err == nil {
			name, err := docker.GetContainerName(containerID)
			if err == nil {
				containerNames[containerID] = strings.TrimLeft(name, "/")
				containerImages[containerID] = containerInfo.Config.Image
			}
		}
	}

	// Display each container and its routes
	for containerID, domains := range containerGroups {
		// Log container info
		// Safety check to prevent slice bounds panic
		containerIDShort := containerID
		if len(containerID) >= 12 {
			containerIDShort = containerID[:12]
		}

		containerName := containerID
		if name, exists := containerNames[containerID]; exists {
			containerName = name
		}

		containerImage := "unknown"
		if image, exists := containerImages[containerID]; exists {
			containerImage = image
		}

		info := containerInfo[containerID]

		logger.Info(fmt.Sprintf("Container: %s (%s)", containerName, containerIDShort),
			"ip", info.ContainerIP,
			"image", containerImage)

		// Log each domain for this container
		for i, domain := range domains {
			// Check if i is within bounds of the protocol and port arrays
			protocol := "unknown"
			port := "unknown"

			if i < len(info.Protocols) {
				protocol = info.Protocols[i]
			}

			if i < len(info.Ports) {
				port = info.Ports[i]
			}

			logger.Info(fmt.Sprintf("  ├─ Domain: %s", domain),
				"protocol", protocol,
				"port", port)
		}
	}

	logger.Info("---- End of active containers and routes list ----")
	return nil
}

// FindRoutesByOldName tries to find routes that might have been associated with a container
// that was recreated with the same name but a new ID
func (p *Proxy) FindRoutesByOldName(containerName string) map[string]*ProxyRouteInfo {
	result := make(map[string]*ProxyRouteInfo)

	// Query the database with retry to find if any route might be associated with this container name
	rows, err := p.dbQueryWithRetry(p.queries.GetAllActiveRoutesWithDetails)
	if err != nil {
		logger.Error("Failed to query database for routes by old name", "error", err)
		return result
	}
	defer rows.Close()

	// Check each route to see if it might be associated with the container name
	for rows.Next() {
		var domain, containerID, containerIP, containerPort, protocol, path string
		var active bool
		if err := rows.Scan(&domain, &containerID, &containerIP, &containerPort, &protocol, &path, &active); err != nil {
			logger.Error("Failed to scan row", "error", err)
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
							Active:        active,
						}
					}
				}
			}
		} else {
			// We found an existing container - if it has the same name, this is interesting
			existingContainerName := strings.TrimPrefix(name, "/")
			if existingContainerName == containerName {
				// This is an exact match - shouldn't happen if we're looking for recreated containers
				// but include it anyway
				result[domain] = &ProxyRouteInfo{
					Domain:        domain,
					ContainerID:   containerID,
					ContainerIP:   containerIP,
					ContainerPort: containerPort,
					Protocol:      protocol,
					Path:          path,
					Active:        active,
				}
			}
		}
	}

	return result
}

// markRoutesInactive marks the specified routes as inactive in the database and memory
func (p *Proxy) markRoutesInactive(domains []string) {
	if len(domains) == 0 {
		return
	}

	// Get the admin domain to protect it
	adminDomain := p.app.GetConfig().Http.FullDomain()

	// Filter out admin domain from domains to mark inactive
	filteredDomains := make([]string, 0, len(domains))
	for _, domain := range domains {
		if domain != adminDomain {
			filteredDomains = append(filteredDomains, domain)
		} else {
			logger.Warn("Prevented admin domain from being marked inactive",
				"domain", adminDomain)
		}
	}

	// If all domains were filtered out, nothing to do
	if len(filteredDomains) == 0 {
		return
	}

	// Use the filtered domains from here on
	domains = filteredDomains

	// Begin a transaction
	tx, err := p.app.GetDB().Begin()
	if err != nil {
		logger.Error("Failed to begin transaction for marking routes inactive", "error", err)
		return
	}
	defer tx.Rollback()

	// Update each route in the database
	for _, domain := range domains {
		// First, get the domain ID
		var domainID string
		err := tx.QueryRow(p.queries.GetDomainByName, domain).Scan(&domainID)
		if err != nil {
			logger.Error("Failed to get domain ID for inactive route", "domain", domain, "error", err)
			continue
		}

		// Update the route to mark it as inactive
		now := time.Now().Format(time.RFC3339)
		_, err = tx.Exec(p.queries.MarkRouteInactive, now, domainID)
		if err != nil {
			logger.Error("Failed to mark route as inactive in database", "domain", domain, "error", err)
			continue
		}

		logger.Info("Marked route as inactive", "domain", domain)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		logger.Error("Failed to commit transaction for marking routes inactive", "error", err)
		return
	}

	logger.Info("Successfully marked orphaned routes as inactive", "count", len(domains))
}

// LogActiveContainersAndRoutes logs a list of active containers and their routes
func (p *Proxy) LogActiveContainersAndRoutes() {
	logger.Info("---- Listing active containers and routes ----")

	// Get the admin domain
	adminDomain := p.app.GetConfig().Http.FullDomain()

	// Check if admin route exists in the database
	adminRoute, err := p.GetRouteByDomainName(adminDomain)
	if err != nil {
		logger.Error("Error checking for admin route", "error", err)
	}
	adminExists := adminRoute != nil

	// Group routes by container ID directly
	containerGroups := make(map[string][]string)
	containerInfo := make(map[string]struct {
		ContainerIP string
		ContainerID string
		Protocols   []string
		Ports       []string
	})

	// Get active routes from database and count them
	var activeRoutes int
	rows, err := p.dbQueryWithRetry(p.queries.GetActiveRoutes)
	if err != nil {
		logger.Error("Failed to query database for active routes", "error", err)
		return
	}
	defer rows.Close()

	// Process all routes from the database
	for rows.Next() {
		var id, domain, containerID, containerIP, containerPort, protocol, path string
		var active bool
		if err := rows.Scan(&id, &domain, &containerID, &containerIP, &containerPort, &protocol, &path, &active); err != nil {
			logger.Error("Failed to scan row", "error", err)
			continue
		}

		// Increment active routes counter
		activeRoutes++

		// Group by container ID
		containerGroups[containerID] = append(containerGroups[containerID], domain)

		// Store container info
		info, exists := containerInfo[containerID]
		if !exists {
			info = struct {
				ContainerIP string
				ContainerID string
				Protocols   []string
				Ports       []string
			}{
				ContainerIP: containerIP,
				ContainerID: containerID,
				Protocols:   []string{protocol},
				Ports:       []string{containerPort},
			}
		} else {
			// Add protocol if not already in the list
			protocolExists := false
			for _, p := range info.Protocols {
				if p == protocol {
					protocolExists = true
					break
				}
			}
			if !protocolExists {
				info.Protocols = append(info.Protocols, protocol)
			}

			// Add port if not already in the list
			portExists := false
			for _, p := range info.Ports {
				if p == containerPort {
					portExists = true
					break
				}
			}
			if !portExists {
				info.Ports = append(info.Ports, containerPort)
			}
		}

		containerInfo[containerID] = info
	}

	// Log admin domain status and active routes count
	if !adminExists {
		logger.Warn("Admin domain is missing from routes", "domain", adminDomain)
	}
	logger.Info(fmt.Sprintf("Found %d active routes", activeRoutes))

	// Get container names and images
	containerNames := make(map[string]string)
	containerImages := make(map[string]string)

	for containerID := range containerGroups {
		containerInfo, err := docker.GetContainerInfo(containerID)
		if err == nil {
			name, err := docker.GetContainerName(containerID)
			if err == nil {
				containerNames[containerID] = strings.TrimLeft(name, "/")
				containerImages[containerID] = containerInfo.Config.Image
			}
		}
	}

	// Display each container and its routes
	for containerID, domains := range containerGroups {
		// Log container info
		// Safety check to prevent slice bounds panic
		containerIDShort := containerID
		if len(containerID) >= 12 {
			containerIDShort = containerID[:12]
		}

		containerName := containerID
		if name, exists := containerNames[containerID]; exists {
			containerName = name
		}

		containerImage := "unknown"
		if image, exists := containerImages[containerID]; exists {
			containerImage = image
		}

		info := containerInfo[containerID]

		logger.Info(fmt.Sprintf("Container: %s (%s)", containerName, containerIDShort),
			"ip", info.ContainerIP,
			"image", containerImage)

		// Log each domain for this container
		for i, domain := range domains {
			// Check if i is within bounds of the protocol and port arrays
			protocol := "unknown"
			port := "unknown"

			if i < len(info.Protocols) {
				protocol = info.Protocols[i]
			}

			if i < len(info.Ports) {
				port = info.Ports[i]
			}

			logger.Info(fmt.Sprintf("  ├─ Domain: %s", domain),
				"protocol", protocol,
				"port", port)
		}
	}

	logger.Info("---- End of active containers and routes list ----")
}

// LogGordonIdentity prints information about the Gordon container for debugging
func (p *Proxy) LogGordonIdentity() {
	logger.Debug("Logging Gordon identity information")

	// Print our container ID if available
	if p.gordonContainerID != "" {
		logger.Info("Gordon container ID", "container_id", p.gordonContainerID)

		// Try to get the container name
		containers, err := docker.ListRunningContainers()
		if err == nil {
			for _, container := range containers {
				if container.ID == p.gordonContainerID {
					containerName := strings.TrimLeft(container.Names[0], "/")
					logger.Info("Gordon container name", "container_name", containerName)
					break
				}
			}
		}
	} else {
		logger.Warn("Gordon container ID not available - running outside container?")
	}
}

// DiscoverMissingRoutes finds Gordon-managed containers that don't have routes yet
// and adds them to the routing table
func (p *Proxy) DiscoverMissingRoutes() {
	logger.Info("Discovering containers that might need routes")

	// Get the network name from app config
	networkName := p.app.GetConfig().ContainerEngine.Network
	if networkName == "" {
		logger.Error("Cannot discover missing routes, network name is not configured")
		return
	}

	// Get all containers in the network
	networkInfo, err := docker.GetNetworkInfo(networkName)
	if err != nil {
		logger.Error("Failed to get network info", "error", err)
		return
	}

	// Create a map of container IDs that already have routes by querying the database
	routedContainerIDs := make(map[string]bool)
	rows, err := p.dbQueryWithRetry(p.queries.GetAllContainerIDs)
	if err != nil {
		logger.Error("Failed to query database for container IDs", "error", err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var containerID string
			if err := rows.Scan(&containerID); err != nil {
				logger.Error("Failed to scan container ID row", "error", err)
				continue
			}
			routedContainerIDs[containerID] = true
		}
	}

	// Check each container in the network
	for containerID, containerEndpoint := range networkInfo.Containers {
		// Skip if this container already has a route
		if routedContainerIDs[containerID] {
			continue
		}

		// Get container info to check for gordon labels
		containerInfo, err := docker.GetContainerInfo(containerID)
		if err != nil {
			logger.Debug("Failed to get container info",
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
			logger.Debug("Gordon container without domain label",
				"container_id", containerID,
				"container_name", strings.TrimPrefix(containerInfo.Name, "/"))
			continue
		}

		// Normalize domain (lowercase, no trailing dot)
		domainLabel = strings.TrimSuffix(strings.ToLower(domainLabel), ".")

		// Skip if this is the admin domain
		if domainLabel == p.app.GetConfig().Http.FullDomain() {
			logger.Debug("Skipping auto-discovery for admin domain",
				"domain", domainLabel)
			continue
		}

		// Check if there's already a route for this domain by querying the database
		route, err := p.GetRouteByDomainName(domainLabel)
		if err != nil {
			logger.Error("Error checking for existing route", "error", err)
			continue
		}

		if route != nil {
			logger.Debug("Route already exists for domain",
				"domain", domainLabel)
			continue
		}

		// Get the container IP from the network info
		containerIP := containerEndpoint.IPv4Address
		if containerIP == "" {
			logger.Warn("Container has no IP address",
				"container_id", containerID)
			continue
		}

		// Extract just the IP part from the CIDR notation (e.g., "192.168.1.2/24" → "192.168.1.2")
		containerIP = strings.Split(containerIP, "/")[0]

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
		logger.Info("Adding missing route for Gordon container",
			"domain", domainLabel,
			"container_id", containerID,
			"container_ip", containerIP,
			"container_port", containerPort)

		err = p.AddRoute(domainLabel, containerID, containerIP, containerPort, protocol, "/")
		if err != nil {
			logger.Error("Failed to add missing route", "error", err)
		}
	}

	// Add a debug log for routes after discovery
	var count int
	routes, err := p.dbQueryWithRetry(p.queries.GetAllActiveRoutesWithDetails)
	if err != nil {
		logger.Error("Failed to count active routes", "error", err)
	}
	defer routes.Close()
	for routes.Next() {
		var domain, containerID, containerIP, containerPort, protocol, path string
		var active bool
		if err := routes.Scan(&domain, &containerID, &containerIP, &containerPort, &protocol, &path, &active); err != nil {
			logger.Error("Failed to scan row", "error", err)
			continue
		}
		count++
	}
	logger.Debug("Routes after discovery", "count", count)
}

// HandleContainerRecreation handles the recreation of containers and updates routes accordingly
func (p *Proxy) HandleContainerRecreation(containerID, containerName, containerIP string) {
	logger.Info("Handling potential container recreation",
		"container_id", containerID,
		"container_name", containerName,
		"container_ip", containerIP)

	// Get container info to check for gordon labels
	containerInfo, err := docker.GetContainerInfo(containerID)
	if err != nil {
		logger.Warn("Failed to get container info", "error", err)
		return
	}

	// Check if this is a Gordon-managed container
	if _, exists := containerInfo.Config.Labels["gordon.managed"]; !exists {
		logger.Debug("Ignoring non-Gordon container", "container_name", containerName)
		return
	}

	// First approach: check if there are any domains that should point to this container based on labels
	if domainLabel, exists := containerInfo.Config.Labels["gordon.domain"]; exists && domainLabel != "" {
		// Normalize domain (lowercase, no trailing dot)
		domainLabel = strings.TrimSuffix(strings.ToLower(domainLabel), ".")

		// Check if there's already a route for this domain
		route, err := p.GetRouteByDomainName(domainLabel)
		if err != nil {
			logger.Error("Error checking if domain exists", "domain", domainLabel, "error", err)
		}

		if route != nil {
			// Route exists - check if it's for a different container ID
			if route.ContainerID != containerID {
				logger.Info("Container was recreated with the same domain label",
					"domain", domainLabel,
					"old_container_id", route.ContainerID,
					"new_container_id", containerID)

				// Get the port to use for the proxy from the container labels
				containerPort := "80" // Default
				if portLabel, exists := containerInfo.Config.Labels["gordon.proxy.port"]; exists && portLabel != "" {
					// But prefer the container label if present
					containerPort = portLabel
				}

				// Get the protocol to use
				protocol := "http" // Default
				if sslLabel, exists := containerInfo.Config.Labels["gordon.proxy.ssl"]; exists {
					// But prefer the container label if present
					if sslLabel == "true" || sslLabel == "1" || sslLabel == "yes" {
						protocol = "https"
					} else {
						protocol = "http"
					}
				}

				// Update the route with the new container ID and IP
				err = p.AddRoute(domainLabel, containerID, containerIP, containerPort, protocol, route.Path)
				if err != nil {
					logger.Error("Failed to update route for recreated container", "error", err)
				} else {
					logger.Info("Updated route for recreated container",
						"domain", domainLabel,
						"container_id", containerID,
						"container_ip", containerIP)
				}
			} else {
				// Same container ID but maybe IP changed
				if route.ContainerIP != containerIP {
					logger.Info("Container IP changed",
						"domain", domainLabel,
						"container_id", containerID,
						"old_ip", route.ContainerIP,
						"new_ip", containerIP)

					// Update the IP
					err = p.ForceUpdateRouteIP(domainLabel, containerIP)
					if err != nil {
						logger.Error("Failed to update IP for container", "error", err)
					}
				}
			}
		} else {
			// No existing route for this domain - might be a new container
			logger.Info("New container with domain label",
				"domain", domainLabel,
				"container_id", containerID)

			// Get the port to use for the proxy from the container labels
			containerPort := "80" // Default
			if portLabel, exists := containerInfo.Config.Labels["gordon.proxy.port"]; exists && portLabel != "" {
				// But prefer the container label if present
				containerPort = portLabel
			}

			// Get the protocol to use
			protocol := "http" // Default
			if sslLabel, exists := containerInfo.Config.Labels["gordon.proxy.ssl"]; exists {
				// But prefer the container label if present
				if sslLabel == "true" || sslLabel == "1" || sslLabel == "yes" {
					protocol = "https"
				} else {
					protocol = "http"
				}
			}

			// Add a new route for this container
			logger.Info("Adding missing route for Gordon container",
				"domain", domainLabel,
				"container_id", containerID,
				"container_ip", containerIP,
				"container_port", containerPort)

			err = p.AddRoute(domainLabel, containerID, containerIP, containerPort, protocol, "/")
			if err != nil {
				logger.Error("Failed to add missing route", "error", err)
			}
		}
	}

	// Second approach: check for routes with name-based matching that might need updating
	// This handles the case where the container name is used instead of explicit domain labels
	if domainCheck := p.FindRoutesByOldName(containerName); len(domainCheck) > 0 {
		logger.Info("Found potential routes for recreated container by name matching",
			"container_name", containerName,
			"domains_count", len(domainCheck))

		for domain, oldRoute := range domainCheck {
			logger.Info("Updating route for recreated container by name matching",
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
				logger.Error("Failed to update route for recreated container", "error", err)
			} else {
				logger.Info("Updated route for recreated container",
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

	logger.Info("Starting container event listener", "network", networkName)

	// Create a callback function to handle container events
	containerEventCallback := func(containerID, containerName, containerIP string) {
		logger.Debug("Container event (start/restart) received",
			"container_id", containerID,
			"container_name", containerName,
			"container_ip", containerIP)

		// Use a goroutine to handle the event asynchronously to avoid blocking the event listener
		go func() {
			// Add a small delay to allow any direct API calls to complete first
			time.Sleep(500 * time.Millisecond)

			// Check if this is the Gordon container (admin domain)
			isAdminContainer := false
			adminDomain := p.app.GetConfig().Http.FullDomain()

			// Check if this container is the admin container
			adminRoute, err := p.GetRouteByDomainName(adminDomain)
			if err != nil {
				logger.Error("Error checking for admin container", "error", err)
			} else if adminRoute != nil && adminRoute.ContainerID == containerID {
				isAdminContainer = true
				logger.Debug("Admin container event detected", "container_id", containerID)
			}

			// First, check if there are any existing routes for this container ID
			var existingRoutes = make(map[string]*ProxyRouteInfo)
			// Query the database for routes with this container ID
			rows, err := p.dbQueryWithRetry(p.queries.FindRoutesByContainerID, containerID)
			if err != nil {
				logger.Error("Failed to query database for routes by container ID", "error", err)
			} else {
				defer rows.Close()

				// Collect the routes
				for rows.Next() {
					var id, domain, cID, containerIP, containerPort, protocol, path string
					var active bool
					if err := rows.Scan(&id, &domain, &cID, &containerIP, &containerPort, &protocol, &path, &active); err != nil {
						logger.Error("Failed to scan row", "error", err)
						continue
					}

					existingRoutes[domain] = &ProxyRouteInfo{
						Domain:        domain,
						ContainerID:   cID,
						ContainerIP:   containerIP,
						ContainerPort: containerPort,
						Protocol:      protocol,
						Path:          path,
						Active:        active,
					}
				}
			}

			// If we found existing routes, check if the IP has changed
			if len(existingRoutes) > 0 {
				logger.Debug("Found existing routes for container",
					"container_id", containerID,
					"route_count", len(existingRoutes))

				// Verify the container IP with an additional check
				verifiedIP, err := docker.GetContainerIPFromNetwork(containerID, networkName)
				if err == nil && verifiedIP != "" && verifiedIP != containerIP {
					logger.Warn("Container IP mismatch from event vs. direct network check",
						"container_id", containerID,
						"event_ip", containerIP,
						"network_ip", verifiedIP)

					// Use the directly checked IP as it's more reliable
					containerIP = verifiedIP
				}

				// Check and update each route if IP has changed
				for domain, route := range existingRoutes {
					// Handle admin domain specially to avoid path issues
					if domain == adminDomain && isAdminContainer {
						// For admin domain, only update the IP if it has changed
						if route.ContainerIP != containerIP {
							logger.Info("Admin container restarted with new IP - updating route",
								"container_id", containerID,
								"container_name", containerName,
								"domain", domain,
								"old_ip", route.ContainerIP,
								"new_ip", containerIP)

							// Update IP in database only
							err := p.ForceUpdateRouteIP(domain, containerIP)
							if err != nil {
								logger.Error("Failed to update admin container IP in database", "domain", domain, "error", err)
							} else {
								logger.Info("Updated admin container IP in database",
									"domain", domain,
									"container_id", containerID,
									"new_ip", containerIP)
							}
						}
					} else if route.ContainerIP != containerIP {
						// For non-admin domains, use the standard route update
						logger.Info("Container restarted with new IP - updating route",
							"container_id", containerID,
							"container_name", containerName,
							"domain", domain,
							"old_ip", route.ContainerIP,
							"new_ip", containerIP)

						// Update the route IP
						err = p.ForceUpdateRouteIP(domain, containerIP)
						if err != nil {
							logger.Error("Failed to update IP for restarted container",
								"domain", domain,
								"error", err)
						}
					} else {
						// Even if IP is the same, make sure the route is active
						if !route.Active {
							logger.Info("Reactivating route for restarted container",
								"container_id", containerID,
								"container_name", containerName,
								"domain", domain)

							// Just reuse ForceUpdateRouteIP with the same IP to activate the route
							p.ForceUpdateRouteIP(domain, containerIP)
						}
					}
				}
			}

			// Call the standard recreation handler for additional checks
			p.HandleContainerRecreation(containerID, containerName, containerIP)
			p.LogActiveContainersAndRoutes()
		}()
	}

	// Create a callback function to handle container stop events
	containerStopCallback := func(containerID, containerName string) {
		logger.Info("Container stopped or removed, marking associated routes as inactive",
			"container_id", containerID,
			"container_name", containerName)

		// Find all domains associated with this container ID
		var domains []string
		// Query the database for routes with this container ID
		rows, err := p.dbQueryWithRetry(p.queries.FindRoutesByContainerID, containerID)
		if err != nil {
			logger.Error("Failed to query database for routes by container ID", "error", err)
		} else {
			defer rows.Close()

			// Collect the domains
			for rows.Next() {
				var id, domain, cID, containerIP, containerPort, protocol, path string
				var active bool
				if err := rows.Scan(&id, &domain, &cID, &containerIP, &containerPort, &protocol, &path, &active); err != nil {
					logger.Error("Failed to scan row", "error", err)
					continue
				}
				domains = append(domains, domain)
			}
		}

		if len(domains) > 0 {
			logger.Info("Found routes to mark as inactive", "container_id", containerID, "domains", domains)
			p.markRoutesInactive(domains)
			p.LogActiveContainersAndRoutes()
		} else {
			logger.Debug("No routes found for stopped container", "container_id", containerID)
		}
	}

	// Start the container event listener
	return docker.ListenForContainerEvents(networkName, containerEventCallback, containerStopCallback)
}

// proxyRequest proxies an HTTP request to a container
func (p *Proxy) proxyRequest(c echo.Context, route *ProxyRouteInfo) error {
	host := c.Request().Host
	containerID := route.ContainerID
	containerIP := route.ContainerIP
	containerPort := route.ContainerPort
	// We don't need to use the route's protocol since we always use HTTP for internal connections

	// Check if this is the admin domain
	isAdminDomain := host == p.app.GetConfig().Http.FullDomain()

	// For non-admin domains, verify the container IP is current before proxying
	if !isAdminDomain {
		networkName := p.app.GetConfig().ContainerEngine.Network

		// Get the latest container info to check if IP has changed
		containerInfo, err := docker.GetContainerInfo(containerID)
		if err == nil && containerInfo.NetworkSettings != nil {
			// Check if container has a different IP
			if networkSettings, exists := containerInfo.NetworkSettings.Networks[networkName]; exists &&
				networkSettings.IPAddress != "" && networkSettings.IPAddress != containerIP {
				// Found container with different IP, update the route and use new IP for this request
				logger.Warn("Container IP mismatch detected, using live IP instead of cached value",
					"domain", host,
					"cached_ip", containerIP,
					"live_ip", networkSettings.IPAddress)

				// Use the new IP for this request
				containerIP = networkSettings.IPAddress

				// Update routes in the background
				go func(domain, containerID, containerIP, containerPort string) {
					err := p.ForceUpdateRouteIP(domain, containerIP)
					if err != nil {
						logger.Error("Failed to update IP after mismatch detection",
							"domain", domain,
							"error", err)
					}
				}(host, containerID, containerIP, containerPort)
			}
		}
	}

	// Create the target URL
	targetURL := &url.URL{
		Scheme: "http", // Always use HTTP for internal container connections regardless of external protocol
	}

	// Format host properly for both IPv4 and IPv6
	if strings.Contains(containerIP, ":") {
		// IPv6 address needs to be wrapped in brackets
		containerIP = "[" + containerIP + "]"
	}
	targetURL.Host = fmt.Sprintf("%s:%s", containerIP, containerPort)

	// Create a reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Use our custom transport with longer timeouts
	if p.reverseProxyClient != nil {
		proxy.Transport = p.reverseProxyClient.Transport
	}

	// Set up request director to modify the request before it's sent
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		// Call the original director first
		originalDirector(req)

		// Keep the original host in the Host header
		req.Host = host

		// Set X-Forwarded headers to preserve client information
		if clientIP := c.RealIP(); clientIP != "" {
			req.Header.Set("X-Forwarded-For", clientIP)
			req.Header.Set("X-Real-IP", clientIP)
		}

		// Set X-Forwarded-Proto to the original protocol
		req.Header.Set("X-Forwarded-Proto", c.Scheme())

		// Set X-Forwarded-Host to the original host
		req.Header.Set("X-Forwarded-Host", host)
	}

	// Log errors that occur during proxying
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		logger.Error("Proxy error",
			"host", host,
			"target", targetURL.String(),
			"error", err)

		// Skip container down detection for admin domain
		if !isAdminDomain {
			// Check if this is a connection refused error
			if strings.Contains(err.Error(), "connection refused") ||
				strings.Contains(err.Error(), "no route to host") ||
				strings.Contains(err.Error(), "i/o timeout") {
				// Container might be down or unreachable - mark the route as inactive
				logger.Warn("Container appears to be down, marking route as inactive",
					"domain", host,
					"container_id", containerID)

				// Find domains for this container and mark them inactive
				var domains []string
				// Query the database for routes with this container ID
				rows, err := p.dbQueryWithRetry(p.queries.FindRoutesByContainerID, containerID)
				if err != nil {
					logger.Error("Failed to query database for routes by container ID", "error", err)
				} else {
					defer rows.Close()

					// Collect the domains
					for rows.Next() {
						var id, domain, cID, containerIP, containerPort, protocol, path string
						var active bool
						if err := rows.Scan(&id, &domain, &cID, &containerIP, &containerPort, &protocol, &path, &active); err != nil {
							logger.Error("Failed to scan row", "error", err)
							continue
						}
						domains = append(domains, domain)
					}
				}

				if len(domains) > 0 {
					go p.markRoutesInactive(domains)
				}
			}
		}

		// Return a 502 Bad Gateway error
		c.Response().WriteHeader(http.StatusBadGateway)
		c.Response().Write([]byte("Bad Gateway: Container is unreachable"))
	}

	// Serve the request using the reverse proxy
	proxy.ServeHTTP(c.Response(), c.Request())
	return nil
}

// GetRouteByDomainName gets a route by domain name from the database
func (p *Proxy) GetRouteByDomainName(domainName string) (*ProxyRouteInfo, error) {
	// Normalize domain (lowercase, no trailing dot)
	domainName = strings.TrimSuffix(strings.ToLower(domainName), ".")

	// Query the database for the route
	row := p.app.GetDB().QueryRow(p.queries.GetRouteByDomainName, domainName)

	var id, containerID, containerIP, containerPort, protocol, path string
	var active bool
	if err := row.Scan(&id, &containerID, &containerIP, &containerPort, &protocol, &path, &active); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // No route found
		}
		return nil, fmt.Errorf("failed to scan row: %w", err)
	}

	// Create and return the route
	return &ProxyRouteInfo{
		Domain:        domainName,
		ContainerID:   containerID,
		ContainerIP:   containerIP,
		ContainerPort: containerPort,
		Protocol:      protocol,
		Path:          path,
		Active:        active,
	}, nil
}

// RemoveRoute removes a route from the database and reloads the proxy
func (p *Proxy) RemoveRoute(domainName string) error {
	logger.Info("Removing route", "domain", domainName)

	// First try to get the domain ID
	var domainID string
	err := p.app.GetDB().QueryRow(p.queries.GetDomainByName, domainName).Scan(&domainID)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("domain '%s' not found", domainName)
		}
		return fmt.Errorf("failed to get domain ID: %w", err)
	}

	// Begin transaction
	tx, err := p.app.GetDB().Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Mark the route inactive
	now := time.Now().Format(time.RFC3339)
	_, err = tx.Exec(p.queries.MarkRouteInactive, now, domainID)
	if err != nil {
		return fmt.Errorf("failed to mark route inactive: %w", err)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	logger.Info("Route removed", "domain", domainName)

	// Reload the proxy
	err = p.Reload()
	if err != nil {
		return fmt.Errorf("error reloading proxy after route removal: %w", err)
	}

	return nil
}
