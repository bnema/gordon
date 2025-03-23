package proxy

import (
	"crypto/rand"
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
	"github.com/docker/docker/api/types"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// proxyGenerateUUID generates a random UUID for use in various identifiers
// This is a renamed version to avoid conflicts until we clean up proxy.go
func proxyGenerateUUID() string {
	id, err := uuid.NewRandom()
	if err != nil {
		// If we can't generate a UUID using the standard library,
		// fall back to our own implementation
		b := make([]byte, 16)
		_, err := rand.Read(b)
		if err != nil {
			return fmt.Sprintf("%d", time.Now().UnixNano())
		}
		return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
	}
	return id.String()
}

// normalizeDomain converts a domain to lowercase and removes trailing dot
func normalizeDomain(domain string) string {
	return strings.TrimSuffix(strings.ToLower(domain), ".")
}

// createProxyHandler creates a standard Echo handler function for both HTTP and HTTPS servers
func (p *Proxy) createProxyHandler() echo.HandlerFunc {
	return func(c echo.Context) error {
		host := c.Request().Host

		// Strip port if present in the host
		if hostParts := strings.Split(host, ":"); len(hostParts) > 1 {
			host = hostParts[0]
		}

		// Normalize host (trim trailing dot, convert to lowercase)
		host = normalizeDomain(host)

		// Check if this is the admin domain
		adminDomain := p.app.GetConfig().Http.FullDomain()
		if strings.EqualFold(host, adminDomain) {
			// Always prioritize admin domain
			adminRoute, adminOk := p.routes[adminDomain]

			if adminOk {
				return p.proxyRequest(c, adminRoute)
			}
		}

		// Important: Get the route freshly from the map for every request
		// This ensures we always use the most up-to-date IP
		route, ok := p.routes[host]

		if !ok {
			// Check if the host is an IP address - silently handle without logging warnings
			if net.ParseIP(host) != nil {
				// For IP-based requests, just return a 404 without logging warnings
				return c.String(http.StatusNotFound, "Domain not found")
			}

			// Create list of available domains for debugging
			availableDomains := make([]string, 0, len(p.routes))
			for d := range p.routes {
				availableDomains = append(availableDomains, d)
			}

			// Log warning for non-IP hosts that aren't configured
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
	}
}

// createReverseProxy creates a reverse proxy for a given target URL
func (p *Proxy) createReverseProxy(targetURL *url.URL, host string, containerID string, isAdminDomain bool) *httputil.ReverseProxy {
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
		if clientIP := req.Header.Get("X-Real-IP"); clientIP != "" {
			req.Header.Set("X-Forwarded-For", clientIP)
		}

		// Set X-Forwarded-Proto based on the request scheme
		if req.TLS != nil {
			req.Header.Set("X-Forwarded-Proto", "https")
		} else {
			req.Header.Set("X-Forwarded-Proto", "http")
		}

		// Set X-Forwarded-Host to the original host
		req.Header.Set("X-Forwarded-Host", host)
	}

	// Create error handler for proxy errors
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
		rw.WriteHeader(http.StatusBadGateway)
		rw.Write([]byte("Bad Gateway: Container is unreachable"))
	}

	return proxy
}

// saveAdminRoute preserves the admin route during route operations
func (p *Proxy) saveAdminRoute() *ProxyRouteInfo {
	adminDomain := p.app.GetConfig().Http.FullDomain()
	if route, exists := p.routes[adminDomain]; exists {
		return &ProxyRouteInfo{
			Domain:        route.Domain,
			ContainerID:   route.ContainerID,
			ContainerIP:   route.ContainerIP,
			ContainerPort: route.ContainerPort,
			Protocol:      route.Protocol,
			Path:          route.Path,
			Active:        route.Active,
		}
	}
	return nil
}

// restoreAdminRoute adds back the admin route to the routes map
func (p *Proxy) restoreAdminRoute(adminRoute *ProxyRouteInfo, routes map[string]*ProxyRouteInfo) {
	if adminRoute != nil {
		adminDomain := p.app.GetConfig().Http.FullDomain()
		routes[adminDomain] = adminRoute
		logger.Debug("Restored admin domain route", "domain", adminDomain)
	}
}

// createAdminRoute creates a new admin route with default settings
func (p *Proxy) createAdminRoute() *ProxyRouteInfo {
	adminDomain := p.app.GetConfig().Http.FullDomain()
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

	return &ProxyRouteInfo{
		Domain:        adminDomain,
		ContainerID:   containerName,
		ContainerIP:   containerIP,
		ContainerPort: p.app.GetConfig().Http.Port,
		Protocol:      "http", // Gordon server uses HTTP internally
		Path:          "/",
		Active:        true,
	}
}

// processNewSubdomain extracts subdomain and parent domain and adds them to the database
func (p *Proxy) processNewSubdomain(tx *sql.Tx, hostname, accountID string, now string) error {
	hostParts := strings.Split(hostname, ".")
	if len(hostParts) >= 3 {
		// This appears to be in format subdomain.domain.tld
		// Extract subdomain and domain parts
		subdomain := hostParts[0]
		domainPart := strings.Join(hostParts[1:], ".")

		// Check if the domain part already exists
		var domainPartID string
		err := tx.QueryRow("SELECT id FROM domain WHERE name = ?", domainPart).Scan(&domainPartID)
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
	return nil
}

// getContainerInfoFromLabels extracts container configuration from Docker labels
func getContainerInfoFromLabels(containerInfo types.ContainerJSON) (string, string, string) {
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

	// Get path if specified
	path := "/" // Default
	if pathLabel, exists := containerInfo.Config.Labels["gordon.proxy.path"]; exists && pathLabel != "" {
		path = pathLabel
	}

	return containerPort, protocol, path
}

// updateRouteForContainer updates or creates a route for a container using label information
func (p *Proxy) updateRouteForContainer(
	domainLabel, containerID, containerIP string,
	containerInfo types.ContainerJSON,
	existingPath string) error {

	// Get container configuration from labels
	containerPort, protocol, path := getContainerInfoFromLabels(containerInfo)

	// Use existing path if provided and path from labels is default
	if existingPath != "" && path == "/" {
		path = existingPath
	}

	// Add or update the route
	logger.Info("Updating route for container",
		"domain", domainLabel,
		"container_id", containerID,
		"container_ip", containerIP)

	return p.AddRoute(domainLabel, containerID, containerIP, containerPort, protocol, path)
}

// processContainerEvent handles a container start/restart event
func (p *Proxy) processContainerEvent(containerID, containerName, containerIP string) {
	logger.Debug("Processing container event",
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

	// Check if this is the admin domain
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
		networkName := p.app.GetConfig().ContainerEngine.Network
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
					p.updateAdminContainerIP(domain, containerID, containerIP)
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
				err := p.ForceUpdateRouteIP(domain, containerIP)
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
}

// updateAdminContainerIP updates the admin container's IP in memory and in the database
func (p *Proxy) updateAdminContainerIP(domain, containerID, containerIP string) {
	logger.Info("Admin container restarted with new IP - updating route",
		"container_id", containerID,
		"domain", domain,
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
		_, err = p.dbExecWithRetry(p.queries.UpdateRouteIP,
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

// formatIPAddress handles proper formatting for both IPv4 and IPv6 addresses
func formatIPAddress(ip string) string {
	if strings.Contains(ip, ":") {
		// IPv6 address needs to be wrapped in brackets
		return "[" + ip + "]"
	}
	return ip
}

// createRouteInfo creates a new ProxyRouteInfo struct from parameters
func createRouteInfo(domain, containerID, containerIP, containerPort, protocol, path string, active bool) *ProxyRouteInfo {
	return &ProxyRouteInfo{
		Domain:        domain,
		ContainerID:   containerID,
		ContainerIP:   containerIP,
		ContainerPort: containerPort,
		Protocol:      protocol,
		Path:          path,
		Active:        active,
	}
}

// forceUpdateRouteIPInMemory updates a route's IP address in the in-memory routes map
func (p *Proxy) forceUpdateRouteIPInMemory(domain, newIP string) {
	p.mu.Lock()
	defer p.mu.Unlock()

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
}

// updateRouteIPInDatabase updates a route's IP address in the database
func (p *Proxy) updateRouteIPInDatabase(domain, containerID, newIP string) error {
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

// verifyAndNormalizeContainerIP verifies that a container's IP address is correct
// and returns the normalized IP address
func (p *Proxy) verifyAndNormalizeContainerIP(containerID, providedIP string) string {
	// Double-check that the container exists and is running
	containerInfo, err := docker.GetContainerInfo(containerID)
	if err != nil {
		logger.Warn("Container not found when updating route IP, might have been recreated",
			"container_id", containerID,
			"error", err)
		// Continue with the update anyway, as the container ID might still be valid
		return providedIP
	}

	logger.Debug("Container exists when updating route IP",
		"container_id", containerID,
		"container_state", containerInfo.State.Status)

	// Verify the new IP matches what's in the container
	networkName := p.app.GetConfig().ContainerEngine.Network
	if containerInfo.NetworkSettings != nil && containerInfo.NetworkSettings.Networks != nil {
		if networkSettings, exists := containerInfo.NetworkSettings.Networks[networkName]; exists &&
			networkSettings.IPAddress != "" && networkSettings.IPAddress != providedIP {
			logger.Warn("New IP doesn't match container network IP, using container's actual IP",
				"provided_ip", providedIP,
				"container_actual_ip", networkSettings.IPAddress)
			return networkSettings.IPAddress
		}
	}

	return providedIP
}

// filterOutAdminDomain removes the admin domain from a list of domains
func (p *Proxy) filterOutAdminDomain(domains []string) []string {
	if len(domains) == 0 {
		return domains
	}

	// Get the admin domain to protect it
	adminDomain := p.app.GetConfig().Http.FullDomain()

	// Filter out admin domain from domains
	filteredDomains := make([]string, 0, len(domains))
	for _, domain := range domains {
		if domain != adminDomain {
			filteredDomains = append(filteredDomains, domain)
		} else {
			logger.Warn("Prevented admin domain from being modified",
				"domain", adminDomain)
		}
	}

	return filteredDomains
}

// markRouteInactiveInDatabase marks a route as inactive in the database
func (p *Proxy) markRouteInactiveInDatabase(tx *sql.Tx, domain string) error {
	// Get the domain ID
	var domainID string
	err := tx.QueryRow(p.queries.GetDomainByName, domain).Scan(&domainID)
	if err != nil {
		logger.Error("Failed to get domain ID for inactive route", "domain", domain, "error", err)
		return err
	}

	// Update the route to mark it as inactive
	now := time.Now().Format(time.RFC3339)
	_, err = tx.Exec(p.queries.MarkRouteInactive, now, domainID)
	if err != nil {
		logger.Error("Failed to mark route as inactive in database", "domain", domain, "error", err)
		return err
	}

	return nil
}

// markRouteInactiveInMemory marks a route as inactive in the in-memory routes map
func (p *Proxy) markRouteInactiveInMemory(domain string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if route, exists := p.routes[domain]; exists {
		route.Active = false
		logger.Info("Marked route as inactive", "domain", domain)
	}
}

// getActiveRoutes returns a copy of all active routes in the routes map
func (p *Proxy) getActiveRoutes() map[string]*ProxyRouteInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

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

	return routesToCheck
}

// checkAndUpdateIPIfNeeded checks if a container's IP address has changed and updates it if needed
func (p *Proxy) checkAndUpdateIPIfNeeded(domain string, route *ProxyRouteInfo, networkName string) {
	// Skip checking admin domain
	if domain == p.app.GetConfig().Http.FullDomain() {
		return
	}

	// Verify the current container IP
	currentIP, err := docker.GetContainerIPFromNetwork(route.ContainerID, networkName)
	if err != nil {
		// Container might not exist anymore
		logger.Warn("Failed to get current IP for container during consistency check",
			"domain", domain,
			"container_id", route.ContainerID,
			"error", err)
		return
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

// checkAdminDomainProtection checks if a domain is the admin domain and returns an error if it is
func (p *Proxy) checkAdminDomainProtection(domain string) error {
	adminDomain := p.app.GetConfig().Http.FullDomain()
	if domain == adminDomain {
		logger.Warn("Attempt to modify admin domain prevented", "domain", domain)
		return fmt.Errorf("cannot modify admin domain: %s", domain)
	}
	return nil
}

// deleteRouteWithTransaction deletes a route and domain from the database within a transaction
func (p *Proxy) deleteRouteWithTransaction(tx *sql.Tx, domainID string) error {
	// Delete the route with retry
	_, err := txExecWithRetry(tx, p.queries.DeleteRouteByDomainID, domainID)
	if err != nil {
		return fmt.Errorf("failed to delete route: %w", err)
	}

	// Delete the domain with retry
	_, err = txExecWithRetry(tx, p.queries.DeleteDomainByID, domainID)
	if err != nil {
		return fmt.Errorf("failed to delete domain: %w", err)
	}

	return nil
}
