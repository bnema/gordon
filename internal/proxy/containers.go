package proxy

import (
	"os"
	"strings"

	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/logger"
)

// scanContainersAndCheckCertificates scans all containers from the Gordon network
// and checks if we have certificates for their domains and subdomains
func (p *Proxy) scanContainersAndCheckCertificates() error {
	networkName := p.app.GetConfig().ContainerEngine.Network
	if networkName == "" {
		// If no network is defined, skip container scanning
		logger.Info("No container network defined, skipping certificate checks")
		return nil
	}

	logger.Info("Scanning containers in network for certificate checks",
		"network", networkName)

	// Try to get network info using the docker client
	networkInfo, err := docker.GetNetworkInfo(networkName)
	if err != nil {
		logger.Warn("Failed to get network info, skipping certificate checks",
			"error", err,
			"network", networkName)
		return nil // Return nil instead of error to continue execution
	}

	// Skip admin cert check if environment variable is set
	skipAdminCertCheck := os.Getenv("GORDON_SKIP_ADMIN_CERT_CHECK") == "true"
	if skipAdminCertCheck {
		logger.Info("Skipping admin certificate check as GORDON_SKIP_ADMIN_CERT_CHECK=true")
	}

	// Get all containers in the network
	for containerID, containerEndpoint := range networkInfo.Containers {
		containerName := strings.TrimPrefix(containerEndpoint.Name, "/")

		// Get container info to check for relevant domains
		containerInfo, err := docker.GetContainerInfo(containerID)
		if err != nil {
			logger.Warn("Failed to get container info",
				"containerID", containerID,
				"containerName", containerName,
				"error", err)
			continue
		}

		// Extract domains from container
		// First check if there are labels with domain information
		domains := []string{}

		// Check common domain-related labels
		if domainLabel, exists := containerInfo.Config.Labels["gordon.domain"]; exists && domainLabel != "" {
			// Remove any leading dot from domain label
			domainLabel = strings.TrimPrefix(domainLabel, ".")

			// Always add the base domain
			domains = append(domains, domainLabel)

			// Also check if there's a service label to use as a subdomain
			if serviceLabel, serviceExists := containerInfo.Config.Labels["gordon.service"]; serviceExists && serviceLabel != "" {
				// Combine service name with domain to create service.domain format
				serviceSubdomain := serviceLabel + "." + domainLabel
				domains = append(domains, serviceSubdomain)
				logger.Debug("Created additional domain from service label",
					"domain", serviceSubdomain,
					"service", serviceLabel,
					"base_domain", domainLabel)
			}
		}

		// Check environment variables for domain information
		for _, env := range containerInfo.Config.Env {
			if strings.HasPrefix(env, "DOMAIN=") || strings.HasPrefix(env, "VIRTUAL_HOST=") {
				parts := strings.SplitN(env, "=", 2)
				if len(parts) == 2 && parts[1] != "" {
					// Multiple domains might be comma-separated
					for _, domain := range strings.Split(parts[1], ",") {
						// Remove any leading dot before adding the domain
						cleanDomain := strings.TrimPrefix(strings.TrimSpace(domain), ".")
						domains = append(domains, cleanDomain)
					}
				}
			}
		}

		// If no domains were found, use the container name
		if len(domains) == 0 {
			// Check if there is a proxy route for this container
			var found bool
			for domain, routeInfo := range p.routes {
				if routeInfo.ContainerID == containerID {
					domains = append(domains, domain)
					found = true
				}
			}

			// If no route was found, use container name as fallback
			if !found {
				// Use container name as subdomain of the default domain
				if defaultDomain := p.app.GetConfig().Http.Domain; defaultDomain != "" {
					domains = append(domains, containerName+"."+defaultDomain)
				}
			}
		}

		// Check certificates for each domain
		for _, domain := range domains {
			// Check certificate for the full domain
			hasCert := p.checkCertificateInCache(domain)

			if hasCert {
				logger.Debug("Certificate exists for domain",
					"domain", domain,
					"container", containerName)
			} else {
				logger.Info("Certificate not found in cache, requesting",
					"domain", domain,
					"container", containerName)

				// Request certificate (non-blocking)
				go func(domain string) {
					// Set flag to indicate we're processing a specific domain
					p.processingSpecificDomain = true
					// Try to request certificate for domain
					p.requestDomainCertificate(domain)
					// Reset flag when done
					p.processingSpecificDomain = false
				}(domain)
			}

			// If this is a subdomain, check the parent domain too
			parts := strings.Split(domain, ".")
			if len(parts) >= 3 {
				// This is a subdomain (e.g., sub.domain.com)
				parentDomain := strings.Join(parts[1:], ".")

				// Only check parent domain if it's not an ordinary TLD
				if len(parts) > 2 && !isCommonTLD(parts[len(parts)-1]) {
					hasCert = p.checkCertificateInCache(parentDomain)
					if !hasCert {
						logger.Info("Certificate not found for parent domain",
							"domain", parentDomain,
							"container", containerName)

						// Request certificate for parent domain also
						go func(domain string) {
							// Set flag to indicate we're processing a specific domain
							p.processingSpecificDomain = true
							p.requestDomainCertificate(domain)
							// Reset flag when done
							p.processingSpecificDomain = false
						}(parentDomain)
					}
				}
			}
		}
	}

	return nil
}

// isCommonTLD checks if a string is a common top-level domain
func isCommonTLD(tld string) bool {
	commonTLDs := map[string]bool{
		"com": true, "org": true, "net": true, "io": true, "co": true,
		"ai": true, "app": true, "dev": true, "edu": true, "gov": true,
		"info": true, "name": true, "biz": true, "me": true,
	}
	return commonTLDs[tld]
}
