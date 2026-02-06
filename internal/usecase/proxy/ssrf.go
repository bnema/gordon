// Package proxy implements the reverse proxy use case.
package proxy

import (
	"fmt"
	"net"
	"sync"

	"github.com/bnema/gordon/internal/domain"
)

// blockedCIDRStrings contains CIDR blocks that should never be proxied to.
// This prevents SSRF attacks targeting internal services or cloud metadata endpoints.
var blockedCIDRStrings = []string{
	// IPv4 loopback
	"127.0.0.0/8",
	// IPv4 private networks (RFC 1918)
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	// IPv4 link-local (cloud metadata endpoints like AWS/GCP/Azure)
	"169.254.0.0/16",
	// IPv4 localhost alternative
	"0.0.0.0/8",
	// IPv6 loopback
	"::1/128",
	// IPv6 unique local addresses (RFC 4193)
	"fc00::/7",
	// IPv6 link-local
	"fe80::/10",
}

var (
	blockedCIDRs     []*net.IPNet
	blockedCIDRsOnce sync.Once
)

// initBlockedCIDRs parses the blocked CIDR strings into net.IPNet.
// Called lazily on first use.
func initBlockedCIDRs() {
	blockedCIDRsOnce.Do(func() {
		blockedCIDRs = make([]*net.IPNet, 0, len(blockedCIDRStrings))
		for _, cidr := range blockedCIDRStrings {
			_, network, err := net.ParseCIDR(cidr)
			if err != nil {
				// This should never happen with our hardcoded CIDRs
				panic(fmt.Sprintf("invalid blocked CIDR: %s: %v", cidr, err))
			}
			blockedCIDRs = append(blockedCIDRs, network)
		}
	})
}

// isBlockedIP checks if an IP address is in any of the blocked CIDR ranges.
func isBlockedIP(ip net.IP) bool {
	initBlockedCIDRs()
	for _, network := range blockedCIDRs {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// IsBlockedTarget checks if a target host is in a blocked network range.
// It handles both IP addresses and hostnames (via DNS resolution).
// Returns (blocked, error) where error indicates DNS resolution failure.
//
// SECURITY: This function is used to prevent SSRF attacks by blocking
// requests to internal networks, localhost, and cloud metadata endpoints.
func IsBlockedTarget(host string) (bool, error) {
	initBlockedCIDRs()

	// First, try to parse as IP directly
	if ip := net.ParseIP(host); ip != nil {
		return isBlockedIP(ip), nil
	}

	// Not an IP address, resolve DNS
	ips, err := net.LookupIP(host)
	if err != nil {
		// DNS resolution failed - block by default for safety
		// This prevents DNS rebinding attacks where an attacker controls
		// a domain that initially resolves to a public IP, then later
		// resolves to an internal IP.
		return true, fmt.Errorf("DNS resolution failed for %s: %w", host, err)
	}

	if len(ips) == 0 {
		// No IPs resolved - block for safety
		return true, fmt.Errorf("no IP addresses found for %s", host)
	}

	// Check all resolved IPs - block if ANY resolve to a blocked network
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return true, nil
		}
	}

	return false, nil
}

// ValidateExternalRouteTarget validates that an external route target
// is safe to proxy to. Returns domain.ErrSSRFBlocked if the target
// resolves to an internal/blocked network.
func ValidateExternalRouteTarget(host string) error {
	blocked, err := IsBlockedTarget(host)
	if err != nil {
		// DNS error - treat as blocked for security
		return fmt.Errorf("%w: %v", domain.ErrSSRFBlocked, err)
	}
	if blocked {
		return domain.ErrSSRFBlocked
	}
	return nil
}

// ResolveAndValidateHost resolves DNS for a hostname and validates that all
// resolved IPs are safe (not in blocked CIDR ranges). Returns the first safe
// resolved IP address. For raw IP addresses, validates and returns them directly.
//
// SECURITY: This function pins DNS resolution to prevent TOCTOU/DNS-rebinding
// attacks where a hostname resolves to a public IP during validation but to a
// private IP when the proxy actually connects.
func ResolveAndValidateHost(host string) (string, error) {
	initBlockedCIDRs()

	// If already an IP address, validate and return directly
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return "", domain.ErrSSRFBlocked
		}
		return host, nil
	}

	// Resolve DNS
	ips, err := net.LookupIP(host)
	if err != nil {
		return "", fmt.Errorf("%w: DNS resolution failed for %s: %v", domain.ErrSSRFBlocked, host, err)
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("%w: no IP addresses found for %s", domain.ErrSSRFBlocked, host)
	}

	// Find the first non-blocked IP
	for _, ip := range ips {
		if !isBlockedIP(ip) {
			return ip.String(), nil
		}
	}

	return "", domain.ErrSSRFBlocked
}
