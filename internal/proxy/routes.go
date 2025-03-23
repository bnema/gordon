package proxy

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	docker "github.com/bnema/gordon/pkg/docker"
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

// loadRoutes loads the routes from the database
func (p *Proxy) loadRoutes() error {
	// Lock the routes map
	p.mu.Lock()
	defer p.mu.Unlock()

	logger.Debug("Loading proxy routes from database")

	// Now check if we have a Gordon container ID stored for identity purposes
	p.gordonContainerID = p.detectGordonContainer()
	if p.gordonContainerID != "" {
		logger.Info("Loaded Gordon container ID during route initialization", "container_id", p.gordonContainerID)
	}

	// Save the admin domain route if it exists
	adminRoute := p.saveAdminRoute()

	// Query the database for active proxy routes using retry mechanism
	rows, err := p.dbQueryWithRetry(p.queries.GetActiveRoutes)
	if err != nil {
		if err == sql.ErrNoRows {
			logger.Info("No active proxy routes found")

			// Restore admin route if it was saved
			if adminRoute != nil {
				p.routes = make(map[string]*ProxyRouteInfo)
				p.restoreAdminRoute(adminRoute, p.routes)
				logger.Debug("Restored admin domain route after empty database query", "domain", p.app.GetConfig().Http.FullDomain())
			}

			return nil
		}
		return fmt.Errorf("failed to query database: %w", err)
	}
	defer rows.Close()

	// Clear the routes map but preserve the admin domain route
	p.routes = make(map[string]*ProxyRouteInfo)

	// Restore admin route if it was saved
	p.restoreAdminRoute(adminRoute, p.routes)

	// Populate the routes map
	for rows.Next() {
		var id, domain, containerID, containerIP, containerPort, protocol, path string
		var active bool
		if err := rows.Scan(&id, &domain, &containerID, &containerIP, &containerPort, &protocol, &path, &active); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		// Normalize domain
		domain = normalizeDomain(domain)

		// Add the route to the map
		p.routes[domain] = createRouteInfo(domain, containerID, containerIP, containerPort, protocol, path, active)

		logger.Debug("Loaded proxy route",
			"domain", domain,
			"containerIP", containerIP,
			"containerPort", containerPort,
		)
	}

	logger.Info("Loaded proxy routes", "count", len(p.routes))
	return nil
}

// configureRoutes sets up the HTTP and HTTPS routes
func (p *Proxy) configureRoutes() {
	// Ensure only one goroutine can configure routes at a time
	p.mu.Lock()
	defer p.mu.Unlock()

	// Get the admin domain from the config
	adminDomain := p.app.GetConfig().Http.FullDomain()
	logger.Debug("Configuring proxy routes", "admin_domain", adminDomain)

	// Verify admin domain is in routes - if not, we need to add it
	_, adminRouteExists := p.routes[adminDomain]

	if !adminRouteExists {
		// Create new admin route
		p.routes[adminDomain] = p.createAdminRoute()
		logger.Info("Recreated admin domain route",
			"domain", adminDomain,
			"target", fmt.Sprintf("http://%s:%s", p.routes[adminDomain].ContainerIP, p.routes[adminDomain].ContainerPort))
	} else {
		logger.Debug("Admin domain route exists", "domain", adminDomain)
	}

	// Create handler function for both HTTP and HTTPS servers
	handler := p.createProxyHandler()

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
	// Normalize domain name
	domainName = normalizeDomain(domainName)

	logger.Debug("Adding proxy route",
		"domain", domainName,
		"container_id", containerID,
		"container_ip", containerIP,
		"container_port", containerPort,
		"protocol", protocol,
		"path", path)

	// Check if the container is in cooldown period - if so, let the event listener handle it
	if p.IsContainerInCooldownPeriod(containerID) {
		logger.Info("Container is in cooldown period, letting event listener handle route creation",
			"container_id", containerID,
			"domain", domainName)

		// Add the route to memory immediately for better UX
		p.mu.Lock()
		p.routes[domainName] = createRouteInfo(domainName, containerID, containerIP, containerPort, protocol, path, true)
		p.mu.Unlock()

		// Return success - the event listener will handle the database update
		return nil
	}

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

			// Process subdomain if needed
			p.processNewSubdomain(tx, hostname, accountID, now)
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

	// Register this container to prevent duplicate processing
	p.RegisterNewlyCreatedContainer(containerID)

	// Lock to update the in-memory routes map and reload routes
	p.mu.Lock()

	// Update the in-memory routes map
	hostname = domainName
	p.routes[hostname] = createRouteInfo(hostname, containerID, containerIP, containerPort, protocol, path, true)

	// Reload the routes directly while holding the lock
	// This avoids the race condition from concurrent reload operations
	if err := p.Reload(); err != nil {
		logger.Error("Failed to reload routes after adding route",
			"domain", hostname,
			"error", err)
	}

	// Unlock before continuing with certificate operations
	p.mu.Unlock()

	logger.Info("Added proxy route",
		"domain", hostname,
		"containerIP", containerIP,
		"containerPort", containerPort,
	)

	// Request a certificate if this is an HTTPS route
	if strings.ToLower(protocol) == "https" {
		logger.Info("Requesting Let's Encrypt certificate for new HTTPS route",
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
	// Prevent removing the admin domain
	if err := p.checkAdminDomainProtection(domainName); err != nil {
		return fmt.Errorf("cannot remove admin domain route: %s", domainName)
	}

	// Begin a transaction with retry
	tx, err := p.dbBeginWithRetry()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Get the domain ID
	var domainID string
	err = tx.QueryRow(p.queries.GetDomainByName, domainName).Scan(&domainID)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("domain not found: %s", domainName)
		}
		return fmt.Errorf("failed to query domain: %w", err)
	}

	// Delete the route and domain from the database
	if err := p.deleteRouteWithTransaction(tx, domainID); err != nil {
		return err
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Reload the routes
	if err := p.Reload(); err != nil {
		return fmt.Errorf("failed to reload routes: %w", err)
	}

	logger.Info("Removed proxy route", "domain", domainName)
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
	logger.Info("Reloading proxy routes from database")

	// Get admin domain
	adminDomain := p.app.GetConfig().Http.FullDomain()
	// Normalize the admin domain
	adminDomain = normalizeDomain(adminDomain)

	// Lock for the entire reload operation to prevent concurrent modifications
	p.mu.Lock()
	defer p.mu.Unlock()

	// Save existing admin route if it exists
	adminRoute := p.saveAdminRoute()

	// Create a new route map instead of modifying the existing one
	// This ensures a clean reload without any potential race conditions
	newRoutes := make(map[string]*ProxyRouteInfo)

	// Load routes from the database into the new map
	rows, err := p.app.GetDB().Query(p.queries.GetActiveRoutes)
	if err != nil {
		if err == sql.ErrNoRows {
			logger.Info("No active proxy routes found during reload")
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

			// Normalize domain
			domain = normalizeDomain(domain)

			// Add the route to the new map
			newRoutes[domain] = createRouteInfo(domain, containerID, containerIP, containerPort, protocol, path, active)

			logger.Debug("Loaded route during reload",
				"domain", domain,
				"containerID", containerID,
				"containerIP", containerIP,
				"containerPort", containerPort)
		}
	}

	// Check if admin domain was loaded from the database
	_, adminLoaded := newRoutes[adminDomain]

	// If admin domain wasn't loaded from DB but we had it in memory, restore it
	if !adminLoaded && adminRoute != nil {
		newRoutes[adminDomain] = adminRoute
		logger.Warn("Admin domain route missing from database, restoring from memory",
			"domain", adminDomain)

		// Also try to add it back to the database
		err := p.AddRoute(
			adminDomain,
			adminRoute.ContainerID,
			adminRoute.ContainerIP,
			adminRoute.ContainerPort,
			adminRoute.Protocol,
			adminRoute.Path,
		)

		if err != nil {
			logger.Error("Failed to save admin route to database",
				"error", err,
				"domain", adminDomain)
		} else {
			logger.Info("Re-added admin domain route to database", "domain", adminDomain)
		}
	}

	// If admin domain is still missing, recreate it
	if _, exists := newRoutes[adminDomain]; !exists {
		// Create a new admin route
		adminRoute := p.createAdminRoute()
		newRoutes[adminDomain] = adminRoute

		// Add to database
		err := p.AddRoute(
			adminDomain,
			adminRoute.ContainerID,
			adminRoute.ContainerIP,
			adminRoute.ContainerPort,
			adminRoute.Protocol,
			adminRoute.Path,
		)

		if err != nil {
			logger.Error("Failed to save recreated admin route to database",
				"error", err,
				"domain", adminDomain)
		} else {
			logger.Info("Recreated and saved admin domain route", "domain", adminDomain)
		}
	}

	// Update the routes map with the new one
	p.routes = newRoutes
	// Note: We don't need to call configureRoutes() here because it would acquire the same lock
	// that we already hold. Instead, we do the work directly.

	// Define the handler using our helper function
	handler := p.createProxyHandler()

	// Add the handler to the HTTPS server
	if p.httpsServer != nil {
		p.httpsServer.Any("/*", handler)
	}

	// Also add the handler to the HTTP server for HTTP-01 challenges
	if p.httpServer != nil {
		p.httpServer.Any("/*", handler)
	}

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

	// Verify and normalize the container IP
	newIP = p.verifyAndNormalizeContainerIP(containerID, newIP)

	// First, update the in-memory route immediately
	p.forceUpdateRouteIPInMemory(domain, newIP)

	// Next, update the database
	return p.updateRouteIPInDatabase(domain, containerID, newIP)
}

// FindRoutesByContainerName finds routes associated with a specific container name
func (p *Proxy) FindRoutesByContainerName(containerName string) []string {
	// Get all containers to map IDs to names
	containers, err := docker.ListRunningContainers()
	if err != nil {
		logger.Error("Failed to list containers when finding routes by container name", "error", err)
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
		logger.Debug("Container name not found in running containers", "container_name", containerName)
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

	// Query the database with retry to find if any route might be associated with this container name
	rows, err := p.app.GetDB().Query(p.queries.GetAllRoutes)
	if err != nil {
		logger.Error("Failed to query database for routes by old name", "error", err)
		return result
	}
	defer rows.Close()

	// Check each route to see if it might be associated with the container name
	for rows.Next() {
		var domain, containerID, containerIP, containerPort, protocol, path string
		if err := rows.Scan(&domain, &containerID, &containerIP, &containerPort, &protocol, &path); err != nil {
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
							Active:        true,
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
					Active:        true,
				}
			}
		}
	}

	return result
}

// HandleContainerRecreation handles the recreation of containers and updates routes accordingly
func (p *Proxy) HandleContainerRecreation(containerID, containerName, containerIP string) {
	// Skip processing if this container is in its cooldown period
	if p.IsContainerInCooldownPeriod(containerID) {
		logger.Debug("Skipping container recreation logic - container in cooldown period",
			"container_id", containerID,
			"container_name", containerName)
		return
	}

	// Use a mutex to prevent concurrent processing of the same container
	p.recentContainersMu.Lock()

	// Check if we're already processing this container
	if _, exists := p.recentContainers[containerID]; exists {
		logger.Debug("Skipping container recreation - already being processed",
			"container_id", containerID,
			"container_name", containerName)
		p.recentContainersMu.Unlock()
		return
	}

	// Mark this container as being processed
	p.recentContainers[containerID] = time.Now()
	p.recentContainersMu.Unlock()

	// Ensure we clean up the processing state when done
	defer func() {
		p.recentContainersMu.Lock()
		delete(p.recentContainers, containerID)
		p.recentContainersMu.Unlock()
	}()

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
		// Normalize domain
		domainLabel = normalizeDomain(domainLabel)

		// Check if there's already a route for this domain
		p.mu.RLock()
		route, domainExists := p.routes[domainLabel]
		p.mu.RUnlock()

		if domainExists {
			// Route exists - check if it's for a different container ID
			if route.ContainerID != containerID {
				logger.Info("Container was recreated with the same domain label",
					"domain", domainLabel,
					"old_container_id", route.ContainerID,
					"new_container_id", containerID)

				// Update route for this container
				err = p.updateRouteForContainer(domainLabel, containerID, containerIP, containerInfo, route.Path)
				if err != nil {
					logger.Error("Failed to update route for recreated container", "error", err)
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

			// Add a new route for this container
			err = p.updateRouteForContainer(domainLabel, containerID, containerIP, containerInfo, "")
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

			// Update route for this container, preserving the old path
			err = p.updateRouteForContainer(domain, containerID, containerIP, containerInfo, oldRoute.Path)
			if err != nil {
				logger.Error("Failed to update route for recreated container", "error", err)
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

			// Process the container event
			p.processContainerEvent(containerID, containerName, containerIP)

			// Call the standard recreation handler for additional checks
			p.HandleContainerRecreation(containerID, containerName, containerIP)
		}()
	}

	// Create a callback function to handle container stop events
	containerStopCallback := func(containerID, containerName string) {
		logger.Info("Container stopped or removed, marking associated routes as inactive",
			"container_id", containerID,
			"container_name", containerName)

		// Find all domains associated with this container ID
		var domains []string
		p.mu.RLock()
		for domain, route := range p.routes {
			if route.ContainerID == containerID {
				domains = append(domains, domain)
			}
		}
		p.mu.RUnlock()

		if len(domains) > 0 {
			logger.Info("Found routes to mark as inactive", "container_id", containerID, "domains", domains)
			p.markRoutesInactive(domains)
		} else {
			logger.Debug("No routes found for stopped container", "container_id", containerID)
		}
	}

	// Schedule periodic check for routes with stale IPs
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				logger.Debug("Running periodic check for route IP consistency")
				p.checkRoutesForIPConsistency()
			case <-time.After(24 * time.Hour): // This is just a fallback in case the app is shutting down
				return
			}
		}
	}()

	// Start the container event listener
	return docker.ListenForContainerEvents(networkName, containerEventCallback, containerStopCallback)
}

// checkRoutesForIPConsistency verifies that all active routes have the correct container IP
func (p *Proxy) checkRoutesForIPConsistency() {
	// Get the network name from app config
	networkName := p.app.GetConfig().ContainerEngine.Network

	// Get a copy of all active routes
	routesToCheck := p.getActiveRoutes()

	// Check each active route for IP consistency
	for domain, route := range routesToCheck {
		p.checkAndUpdateIPIfNeeded(domain, route, networkName)
	}
}

// markRoutesInactive marks the specified routes as inactive in the database and memory
func (p *Proxy) markRoutesInactive(domains []string) {
	if len(domains) == 0 {
		return
	}

	// Filter out admin domain from domains to mark inactive
	filteredDomains := p.filterOutAdminDomain(domains)

	// If all domains were filtered out, nothing to do
	if len(filteredDomains) == 0 {
		return
	}

	// Begin a transaction
	tx, err := p.app.GetDB().Begin()
	if err != nil {
		logger.Error("Failed to begin transaction for marking routes inactive", "error", err)
		return
	}
	defer tx.Rollback()

	// Update each route in the database and memory
	for _, domain := range filteredDomains {
		// Update in database
		err := p.markRouteInactiveInDatabase(tx, domain)
		if err != nil {
			// Skip to next domain if there was an error
			continue
		}

		// Update in memory
		p.markRouteInactiveInMemory(domain)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		logger.Error("Failed to commit transaction for marking routes inactive", "error", err)
		return
	}

	logger.Info("Successfully marked orphaned routes as inactive", "count", len(filteredDomains))
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

		// Normalize domain
		domainLabel = normalizeDomain(domainLabel)

		// Skip if this is the admin domain
		if domainLabel == p.app.GetConfig().Http.FullDomain() {
			logger.Debug("Skipping auto-discovery for admin domain",
				"domain", domainLabel)
			continue
		}

		// Check if there's already a route for this domain
		if _, exists := routes[domainLabel]; exists {
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

		// Add a new route for this container
		logger.Info("Adding missing route for Gordon container",
			"domain", domainLabel,
			"container_id", containerID,
			"container_ip", containerIP)

		err = p.updateRouteForContainer(domainLabel, containerID, containerIP, containerInfo, "")
		if err != nil {
			logger.Error("Failed to add missing route", "error", err)
		}
	}

	// Add a debug log for routes after discovery
	count := 0
	p.mu.RLock()
	for range p.routes {
		count++
	}
	p.mu.RUnlock()
	logger.Debug("Routes after discovery", "count", count)
}

// LogActiveContainersAndRoutes logs a list of active containers and their routes
func (p *Proxy) LogActiveContainersAndRoutes() {
	logger.Info("---- Listing active containers and routes ----")

	// Get the admin domain
	adminDomain := p.app.GetConfig().Http.FullDomain()

	p.mu.RLock()
	_, adminExists := p.routes[adminDomain]
	activeRoutes := len(p.routes)

	// Group routes by container ID directly
	containerGroups := make(map[string][]string)
	containerInfo := make(map[string]struct {
		ContainerIP string
		ContainerID string
		Protocols   []string
		Ports       []string
	})

	for domain, route := range p.routes {
		if !route.Active {
			continue
		}

		containerGroups[route.ContainerID] = append(containerGroups[route.ContainerID], domain)

		// Store container info only once per container
		if _, exists := containerInfo[route.ContainerID]; !exists {
			containerInfo[route.ContainerID] = struct {
				ContainerIP string
				ContainerID string
				Protocols   []string
				Ports       []string
			}{
				ContainerIP: route.ContainerIP,
				ContainerID: route.ContainerID,
				Protocols:   []string{route.Protocol},
				Ports:       []string{route.ContainerPort},
			}
		} else {
			info := containerInfo[route.ContainerID]
			info.Protocols = append(info.Protocols, route.Protocol)
			info.Ports = append(info.Ports, route.ContainerPort)
			containerInfo[route.ContainerID] = info
		}
	}
	p.mu.RUnlock()

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

// detectGordonContainer attempts to find the Gordon container automatically
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

// proxyRequest proxies an HTTP request to a container
func (p *Proxy) proxyRequest(c echo.Context, route *ProxyRouteInfo) error {
	host := c.Request().Host
	containerID := route.ContainerID
	containerIP := route.ContainerIP
	containerPort := route.ContainerPort
	targetProtocol := route.Protocol

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
		Scheme: targetProtocol,
		Host:   fmt.Sprintf("%s:%s", formatIPAddress(containerIP), containerPort),
	}

	// Create a reverse proxy using the helper function
	proxy := p.createReverseProxy(targetURL, host, containerID, isAdminDomain)

	// Serve the request using the reverse proxy
	proxy.ServeHTTP(c.Response(), c.Request())
	return nil
}

// RegisterNewlyCreatedContainer adds a container to the recently created list
// to prevent duplicate processing during the cooldown period
func (p *Proxy) RegisterNewlyCreatedContainer(containerID string) {
	p.recentContainersMu.Lock()
	defer p.recentContainersMu.Unlock()

	// Add the container to the recently created list with the current time
	p.recentContainers[containerID] = time.Now()

	// Log the registration
	logger.Debug("Registered container in cooldown period",
		"container_id", containerID,
		"cooldown_period", p.containerCooldownPeriod)

	// Schedule cleanup of old entries
	go func() {
		// Wait for the cooldown period
		time.Sleep(p.containerCooldownPeriod)

		// Remove the container from the recently created list
		p.recentContainersMu.Lock()
		defer p.recentContainersMu.Unlock()

		// Only delete if the timestamp hasn't been updated
		if timestamp, exists := p.recentContainers[containerID]; exists {
			if time.Since(timestamp) >= p.containerCooldownPeriod {
				delete(p.recentContainers, containerID)
				logger.Debug("Removed container from cooldown period", "container_id", containerID)
			}
		}
	}()
}

// IsContainerInCooldownPeriod checks if a container is in its cooldown period
func (p *Proxy) IsContainerInCooldownPeriod(containerID string) bool {
	p.recentContainersMu.RLock()
	defer p.recentContainersMu.RUnlock()

	timestamp, exists := p.recentContainers[containerID]
	if !exists {
		return false
	}

	// Check if the container is still in its cooldown period
	return time.Since(timestamp) < p.containerCooldownPeriod
}
