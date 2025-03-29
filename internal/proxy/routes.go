package proxy

import (
	"crypto/tls"
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
	"github.com/google/uuid"
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
	adminDomain := p.app.GetConfig().Http.FullDomain()
	var adminRoute *ProxyRouteInfo
	if route, exists := p.routes[adminDomain]; exists {
		adminRoute = route
		logger.Debug("Preserving admin domain route", "domain", adminDomain)
	}

	// Query the database for active proxy routes using retry mechanism
	rows, err := p.dbQueryWithRetry(p.Queries.GetActiveRoutes)
	if err != nil {
		if err == sql.ErrNoRows {
			logger.Info("No active proxy routes found")

			// Restore admin route if it was saved
			if adminRoute != nil {
				p.routes = make(map[string]*ProxyRouteInfo)
				p.routes[adminDomain] = adminRoute
				logger.Debug("Restored admin domain route after empty database query", "domain", adminDomain)
			}

			return nil
		}
		return fmt.Errorf("failed to query database: %w", err)
	}
	defer rows.Close()

	// Clear the routes map but preserve the admin domain route
	p.routes = make(map[string]*ProxyRouteInfo)

	// Restore admin route if it was saved
	if adminRoute != nil {
		p.routes[adminDomain] = adminRoute
		logger.Debug("Restored admin domain route after clearing routes map", "domain", adminDomain)
	}

	// Populate the routes map
	for rows.Next() {
		var id, domain, containerID, containerIP, containerPort, protocol, path string
		var active bool
		if err := rows.Scan(&id, &domain, &containerID, &containerIP, &containerPort, &protocol, &path, &active); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		// Normalize domain (lowercase, no trailing dot)
		domain = strings.TrimSuffix(strings.ToLower(domain), ".")

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
	// Get the admin domain from the config
	adminDomain := p.app.GetConfig().Http.FullDomain()
	logger.Debug("Configuring proxy routes", "admin_domain", adminDomain)

	// Verify admin domain is in routes - if not, we need to add it
	p.mu.RLock()
	_, adminRouteExists := p.routes[adminDomain]
	p.mu.RUnlock()

	if !adminRouteExists {
		logger.Warn("Admin domain route is missing, attempting to recreate it")
		p.mu.Lock()
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

		p.routes[adminDomain] = &ProxyRouteInfo{
			Domain:        adminDomain,
			ContainerIP:   containerIP,
			ContainerPort: p.app.GetConfig().Http.Port,
			ContainerID:   containerName,
			Protocol:      "http", // Gordon server uses HTTP internally
			Path:          "/",
			Active:        true,
		}
		logger.Info("Recreated admin domain route",
			"domain", adminDomain,
			"target", fmt.Sprintf("http://%s:%s", containerIP, p.app.GetConfig().Http.Port))
		p.mu.Unlock()
	} else {
		logger.Debug("Admin domain route exists", "domain", adminDomain)
	}

	// Add the handler to the HTTPS server
	if p.httpsServer != nil {
		p.httpsServer.Any("/*", p.proxyRequest)
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

	// Check if the container is in cooldown period - if so, let the event listener handle it
	if p.IsContainerInCooldownPeriod(containerID) {
		logger.Info("Container is in cooldown period, letting event listener handle route creation",
			"container_id", containerID,
			"domain", domainName)

		// Add the route to memory immediately for better UX
		p.mu.Lock()
		p.routes[domainName] = &ProxyRouteInfo{
			Domain:        domainName,
			ContainerID:   containerID,
			ContainerIP:   containerIP,
			ContainerPort: containerPort,
			Protocol:      protocol,
			Path:          path,
			Active:        true,
		}
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
	err = tx.QueryRow(p.Queries.GetFirstAccount).Scan(&accountID)
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
	err = tx.QueryRow(p.Queries.GetDomainByName, hostname).Scan(&domainID)
	if err != nil {
		if err == sql.ErrNoRows {
			// Domain doesn't exist, create it
			domainID = uuid.New().String()
			now := time.Now().Format(time.RFC3339)
			_, err = txExecWithRetry(tx,
				p.Queries.InsertDomain,
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
						domainPartID = uuid.New().String()
						_, err = txExecWithRetry(tx,
							p.Queries.InsertDomain,
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
	err = tx.QueryRow(p.Queries.GetRouteByDomain, domainID).Scan(&existingRouteID, &existingContainerID, &existingContainerPort)

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

		// Use the UpdateRoute query from the queries package
		_, err = txExecWithRetry(tx,
			p.Queries.UpdateRoute,
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
		routeID := uuid.New().String()
		now := time.Now().Format(time.RFC3339)
		_, err = txExecWithRetry(tx,
			p.Queries.InsertRoute,
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

	// Update the in-memory routes map immediately for better UX
	p.mu.Lock()
	p.routes[hostname] = &ProxyRouteInfo{
		Domain:        hostname,
		ContainerID:   containerID,
		ContainerIP:   containerIP,
		ContainerPort: containerPort,
		Protocol:      protocol,
		Path:          path,
		Active:        true,
	}
	p.mu.Unlock()

	// Reload the routes in the background to avoid blocking
	go func() {
		if err := p.Reload(); err != nil {
			logger.Error("Failed to reload routes after adding route",
				"domain", hostname,
				"error", err)
		}
	}()

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
			p.requestCertificateForDomain(domain)
			// Reset the flag after we're done
			p.processingSpecificDomain = false
		}(hostname)
	}

	return nil
}

// RemoveRoute removes a route from the database and reloads the proxy
func (p *Proxy) RemoveRoute(domainName string) error {
	// Prevent removing the admin domain
	adminDomain := p.app.GetConfig().Http.FullDomain()
	if domainName == adminDomain {
		logger.Warn("Attempt to remove admin domain route prevented",
			"domain", domainName)
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
	err = tx.QueryRow(p.Queries.GetDomainByName, domainName).Scan(&domainID)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("domain not found: %s", domainName)
		}
		return fmt.Errorf("failed to query domain: %w", err)
	}

	// Delete the route with retry
	_, err = txExecWithRetry(tx, p.Queries.DeleteRouteByDomainID, domainID)
	if err != nil {
		return fmt.Errorf("failed to delete route: %w", err)
	}

	// Delete the domain with retry
	_, err = txExecWithRetry(tx, p.Queries.DeleteDomainByID, domainID)
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

	// Get admin domain - this should be the full subdomain.domain format
	adminDomain := p.app.GetConfig().Http.FullDomain()
	// Normalize the admin domain (lowercase, no trailing dot)
	adminDomain = strings.TrimSuffix(strings.ToLower(adminDomain), ".")

	// Save existing admin route if it exists
	p.mu.RLock()
	var adminRoute *ProxyRouteInfo
	if route, exists := p.routes[adminDomain]; exists {
		adminRoute = &ProxyRouteInfo{
			Domain:        route.Domain,
			ContainerID:   route.ContainerID,
			ContainerIP:   route.ContainerIP,
			ContainerPort: route.ContainerPort,
			Protocol:      route.Protocol,
			Path:          route.Path,
			Active:        route.Active,
		}
		logger.Debug("Preserved admin domain route during reload", "domain", adminDomain)
	}
	p.mu.RUnlock()

	// Create a new route map instead of modifying the existing one
	// This ensures a clean reload without any potential race conditions
	newRoutes := make(map[string]*ProxyRouteInfo)

	// Load routes from the database into the new map
	rows, err := p.app.GetDB().Query(p.Queries.GetActiveRoutes)
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

			// Normalize domain (lowercase, no trailing dot)
			domain = strings.TrimSuffix(strings.ToLower(domain), ".")

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
		logger.Warn("Admin domain route is missing, recreating it", "domain", adminDomain)

		// Auto-detect the Gordon container information
		containerName := p.detectGordonContainer()
		containerIP := "localhost" // Default to localhost

		// Fall back options for container IP
		if os.Getenv("GORDON_ADMIN_HOST") != "" {
			containerIP = os.Getenv("GORDON_ADMIN_HOST")
		} else if os.Getenv("HOSTNAME") != "" {
			containerIP = os.Getenv("HOSTNAME")
		}

		// Add to in-memory routes
		newRoutes[adminDomain] = &ProxyRouteInfo{
			Domain:        adminDomain,
			ContainerID:   containerName,
			ContainerIP:   containerIP,
			ContainerPort: p.app.GetConfig().Http.Port,
			Protocol:      "http", // Gordon server uses HTTP internally
			Path:          "/",
			Active:        true,
		}

		// Add to database
		err := p.AddRoute(
			adminDomain,
			containerName,
			containerIP,
			p.app.GetConfig().Http.Port,
			"http",
			"/",
		)

		if err != nil {
			logger.Error("Failed to save recreated admin route to database",
				"error", err,
				"domain", adminDomain)
		} else {
			logger.Info("Recreated and saved admin domain route", "domain", adminDomain)
		}
	}

	// Atomically swap the new routes map with the old one
	// This ensures that no requests use a partially updated map
	p.mu.Lock()
	p.routes = newRoutes
	p.mu.Unlock()

	// Rebuild route configuration
	p.configureRoutes()

	if p.httpsServer != nil {
		logger.Debug("Refreshing HTTPS routes")
		p.httpsServer.Routes()
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

	// First, update the in-memory route immediately
	p.mu.Lock()
	if r, ok := p.routes[domain]; ok {
		oldIP := r.ContainerIP
		r.ContainerIP = newIP
		// Make sure the route is also marked as active
		r.Active = true
		logger.Info("Force updated route IP in-memory",
			"domain", domain,
			"old_ip", oldIP,
			"new_ip", newIP)
	}
	p.mu.Unlock()

	// Next, update the database
	// Get the domain ID with retry
	var domainID string
	rows, err := p.app.GetDB().Query(p.Queries.GetDomainByName, domain)
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
	result, err := p.app.GetDB().Exec(p.Queries.UpdateRouteIP, newIP, now, domainID, containerID)

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
	rows, err := p.app.GetDB().Query(p.Queries.GetAllRoutes)
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
		// Normalize domain (lowercase, no trailing dot)
		domainLabel = strings.TrimSuffix(strings.ToLower(domainLabel), ".")

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

		// Skip processing if container is in cooldown period
		if p.IsContainerInCooldownPeriod(containerID) {
			logger.Debug("Skipping container event processing - container in cooldown period",
				"container_id", containerID,
				"container_name", containerName)
			return
		}

		// Register this container as recently created to prevent duplicate processing
		p.RegisterNewlyCreatedContainer(containerID)

		// Use a goroutine to handle the event asynchronously to avoid blocking the event listener
		go func() {
			// Add a small delay to allow any direct API calls to complete first
			time.Sleep(500 * time.Millisecond)

			// Check if this is the Gordon container (admin domain)
			isAdminContainer := false
			adminDomain := p.app.GetConfig().Http.FullDomain()

			p.mu.RLock()
			if route, exists := p.routes[adminDomain]; exists && route.ContainerID == containerID {
				isAdminContainer = true
				logger.Debug("Admin container event detected", "container_id", containerID)
			}
			p.mu.RUnlock()

			// First, check if there are any existing routes for this container ID
			var existingRoutes = make(map[string]*ProxyRouteInfo)
			p.mu.RLock()
			for domain, route := range p.routes {
				if route.ContainerID == containerID {
					existingRoutes[domain] = route
				}
			}
			p.mu.RUnlock()

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

							// Update IP in memory only first
							p.mu.Lock()
							if r, ok := p.routes[domain]; ok {
								r.ContainerIP = containerIP
								r.Active = true
							}
							p.mu.Unlock()

							// Update DB in background
							go func(domain, containerID, containerIP string) {
								// Get the domain ID
								var domainID string
								rows, err := p.dbQueryWithRetry("SELECT id FROM domain WHERE name = ?", domain)
								if err != nil {
									logger.Error("Failed to query domain for admin IP update", "domain", domain, "error", err)
									return
								}

								if !rows.Next() {
									rows.Close()
									logger.Error("Admin domain not found in database", "domain", domain)
									return
								}

								if err := rows.Scan(&domainID); err != nil {
									rows.Close()
									logger.Error("Failed to scan admin domain ID", "domain", domain, "error", err)
									return
								}
								rows.Close()

								// Update the IP in the database directly
								now := time.Now().Format(time.RFC3339)
								_, err = p.dbExecWithRetry(p.Queries.UpdateRouteIP,
									containerIP, now, domainID, containerID)

								if err != nil {
									logger.Error("Failed to update admin container IP in database", "domain", domain, "error", err)
								} else {
									logger.Info("Updated admin container IP in database",
										"domain", domain,
										"container_id", containerID,
										"new_ip", containerIP)
								}
							}(domain, containerID, containerIP)
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
	networkName := p.app.GetConfig().ContainerEngine.Network

	// Get a copy of the current routes
	p.mu.RLock()
	routesToCheck := make(map[string]*ProxyRouteInfo)
	for domain, route := range p.routes {
		if route.Active {
			routesToCheck[domain] = &ProxyRouteInfo{
				Domain:        route.Domain,
				ContainerID:   route.ContainerID,
				ContainerIP:   route.ContainerIP,
				ContainerPort: route.ContainerPort,
				Protocol:      route.Protocol,
				Path:          route.Path,
				Active:        route.Active,
			}
		}
	}
	p.mu.RUnlock()

	// Check each active route
	for domain, route := range routesToCheck {
		// Skip checking admin domain
		if domain == p.app.GetConfig().Http.FullDomain() {
			continue
		}

		// Verify the current container IP
		currentIP, err := docker.GetContainerIPFromNetwork(route.ContainerID, networkName)
		if err != nil {
			// Container might not exist anymore
			logger.Warn("Failed to get current IP for container during consistency check",
				"domain", domain,
				"container_id", route.ContainerID,
				"error", err)
			continue
		}

		// If IP has changed, update the route
		if currentIP != "" && currentIP != route.ContainerIP {
			logger.Info("Detected IP mismatch during consistency check",
				"domain", domain,
				"container_id", route.ContainerID,
				"old_ip", route.ContainerIP,
				"current_ip", currentIP)

			// Update the route
			err = p.ForceUpdateRouteIP(domain, currentIP)
			if err != nil {
				logger.Error("Failed to update IP during consistency check",
					"domain", domain,
					"error", err)
			}
		}
	}
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
		err := tx.QueryRow(p.Queries.GetDomainByName, domain).Scan(&domainID)
		if err != nil {
			logger.Error("Failed to get domain ID for inactive route", "domain", domain, "error", err)
			continue
		}

		// Update the route to mark it as inactive
		now := time.Now().Format(time.RFC3339)
		_, err = tx.Exec(p.Queries.MarkRouteInactive, now, domainID)
		if err != nil {
			logger.Error("Failed to mark route as inactive in database", "domain", domain, "error", err)
			continue
		}

		// Also update the in-memory route
		p.mu.Lock()
		if route, exists := p.routes[domain]; exists {
			route.Active = false
			logger.Info("Marked route as inactive", "domain", domain)
		}
		p.mu.Unlock()
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		logger.Error("Failed to commit transaction for marking routes inactive", "error", err)
		return
	}

	logger.Info("Successfully marked orphaned routes as inactive", "count", len(domains))
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

		// Normalize domain (lowercase, no trailing dot)
		domainLabel = strings.TrimSuffix(strings.ToLower(domainLabel), ".")

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
func (p *Proxy) proxyRequest(c echo.Context) error {
	host := c.Request().Host

	// Strip port if present in the host
	if hostParts := strings.Split(host, ":"); len(hostParts) > 1 {
		host = hostParts[0]
	}

	// Normalize host (trim trailing dot, convert to lowercase)
	host = strings.TrimSuffix(strings.ToLower(host), ".")

	// Debug log the incoming request
	// logger.Debug("Proxy request received",
	// 	"host", host,
	// 	"method", c.Request().Method,
	// 	"path", c.Request().URL.Path,
	// 	"client_ip", c.RealIP())

	// Get the route for this host
	p.mu.RLock()
	route, ok := p.routes[host]
	p.mu.RUnlock()

	if !ok {
		// Check if the host is an IP address
		if net.ParseIP(host) != nil {
			// For IP-based requests, just return a 404 without logging warnings
			return c.String(http.StatusNotFound, "Domain not found")
		}

		// Log available domains for debugging
		availableDomains := make([]string, 0, len(p.routes))
		p.mu.RLock()
		for d := range p.routes {
			availableDomains = append(availableDomains, d)
		}
		p.mu.RUnlock()

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

	return p.doProxyRequest(c, route)
}

func (p *Proxy) doProxyRequest(c echo.Context, route *ProxyRouteInfo) error {
	host := c.Request().Host
	containerID := route.ContainerID
	containerIP := route.ContainerIP
	containerPort := route.ContainerPort
	targetProtocol := route.Protocol

	logger.Debug("Starting proxy request",
		"host", host,
		"container_id", containerID,
		"container_ip", containerIP,
		"container_port", containerPort,
		"protocol", targetProtocol)

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
		Scheme: "http", // Always use HTTP internally, regardless of external protocol
	}

	// Format host properly for both IPv4 and IPv6
	if strings.Contains(containerIP, ":") {
		// IPv6 address needs to be wrapped in brackets
		containerIP = "[" + containerIP + "]"
	}
	targetURL.Host = fmt.Sprintf("%s:%s", containerIP, containerPort)

	logger.Debug("Proxying to backend",
		"target_url", targetURL.String(),
		"original_host", host)

	// Create a reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Use our custom transport with longer timeouts
	if p.reverseProxyClient != nil {
		proxy.Transport = p.reverseProxyClient.Transport
	} else {
		// Create a transport with reasonable timeouts if none exists
		proxy.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
			// Use shorter timeouts for internal network
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			ExpectContinueTimeout: 3 * time.Second,
			// Add a custom dialer with explicit timeouts
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
				DualStack: true,
			}).DialContext,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			ForceAttemptHTTP2:   true,
		}
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

		logger.Debug("Modified outgoing proxy request",
			"req_host", req.Host,
			"req_url", req.URL.String(),
			"x_forwarded_proto", req.Header.Get("X-Forwarded-Proto"))
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
				p.mu.RLock()
				for d, r := range p.routes {
					if r.ContainerID == containerID {
						domains = append(domains, d)
					}
				}
				p.mu.RUnlock()

				if len(domains) > 0 {
					go p.markRoutesInactive(domains)
				}
			}
		}

		// Return a 502 Bad Gateway error
		c.Response().WriteHeader(http.StatusBadGateway)
		c.Response().Write([]byte("Bad Gateway: Container is unreachable"))
	}

	// For admin domain, ensure the connection succeeds
	if isAdminDomain {
		// Do a pre-check to make sure the admin service is reachable
		testURL := fmt.Sprintf("http://%s:%s/admin/ping", containerIP, containerPort)
		client := &http.Client{
			Timeout: 2 * time.Second,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout: 1 * time.Second,
				}).DialContext,
			},
		}

		logger.Debug("Testing admin connection before proxying",
			"test_url", testURL)

		_, err := client.Get(testURL)
		if err != nil {
			logger.Warn("Admin service pre-check failed, trying alternative connection methods",
				"error", err)

			// Try alternative IPs for the admin domain if the container IP isn't working
			altIPs := []string{"localhost", "127.0.0.1", p.app.GetConfig().Http.SubDomain}

			for _, altIP := range altIPs {
				testURL = fmt.Sprintf("http://%s:%s/admin/ping", altIP, containerPort)
				_, err := client.Get(testURL)
				if err == nil {
					logger.Info("Found working admin connection with alternative IP",
						"ip", altIP)
					containerIP = altIP
					targetURL.Host = fmt.Sprintf("%s:%s", containerIP, containerPort)
					break
				}
			}
		}
	}

	// Serve the request using the reverse proxy
	logger.Debug("Serving proxy request",
		"target", targetURL.String())
	proxy.ServeHTTP(c.Response(), c.Request())

	logger.Debug("Proxy request completed",
		"host", host,
		"target", targetURL.String(),
		"status", c.Response().Status)

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

// Add specific domain handling to use our new certificate manager

// requestCertificateForDomain requests a certificate for a specific domain using our new refactored system
func (p *Proxy) requestCertificateForDomain(domain string) {
	// Use the certificate manager stored on the Proxy struct
	if p.certificateManager != nil {
		// Request the certificate using the new manager
		_, err := p.certificateManager.GetCertificate(domain)
		if err != nil {
			logger.Error("Failed to get certificate with new manager",
				"domain", domain,
				"error", err)
		} else {
			logger.Info("Successfully secured certificate for domain",
				"domain", domain)
		}
		return
	}

	// Fall back to old method if new manager not available
	// logger.Debug("Using legacy certificate manager for domain",
	// 	"domain", domain)
	// p.requestDomainCertificateRefactored(domain) // Removed fallback to non-existent method

	// If manager is nil, log an error (should have been caught earlier)
	logger.Error("requestCertificateForDomain called but certificateManager is nil", "domain", domain)
}
