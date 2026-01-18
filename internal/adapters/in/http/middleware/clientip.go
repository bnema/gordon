package middleware

import (
	"net"
	"net/http"
	"strings"
)

// ParseTrustedProxies converts a list of IP addresses and CIDR ranges to net.IPNet.
// It accepts both single IPs (e.g., "10.0.0.1") and CIDR notation (e.g., "10.0.0.0/8").
// Single IPs are converted to /32 (IPv4) or /128 (IPv6) CIDR blocks.
func ParseTrustedProxies(proxies []string) []*net.IPNet {
	var nets []*net.IPNet
	for _, proxy := range proxies {
		// Try parsing as CIDR
		_, ipNet, err := net.ParseCIDR(proxy)
		if err == nil {
			nets = append(nets, ipNet)
			continue
		}
		// Try parsing as single IP
		ip := net.ParseIP(proxy)
		if ip != nil {
			// Convert single IP to /32 or /128 CIDR
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return nets
}

// IsTrustedProxy checks if the given IP address is from a trusted proxy.
// Returns false if trustedNets is empty or the IP cannot be parsed.
func IsTrustedProxy(ip string, trustedNets []*net.IPNet) bool {
	if len(trustedNets) == 0 {
		return false
	}
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false
	}
	for _, ipNet := range trustedNets {
		if ipNet.Contains(parsedIP) {
			return true
		}
	}
	return false
}

// GetClientIP extracts the client IP address from the request.
// It only honors X-Forwarded-For and X-Real-IP headers when the request
// originates from a trusted proxy. This prevents attackers from spoofing
// their IP address when Gordon is exposed directly without a reverse proxy.
func GetClientIP(r *http.Request, trustedNets []*net.IPNet) string {
	// Extract the direct connection IP from RemoteAddr
	remoteIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteIP = r.RemoteAddr
	}

	// Only honor proxy headers if the request comes from a trusted proxy
	if IsTrustedProxy(remoteIP, trustedNets) {
		// Check X-Forwarded-For for proxied requests
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Take first IP in chain (original client)
			if first, _, found := strings.Cut(xff, ","); found {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(xff)
		}

		// Check X-Real-IP
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return xri
		}
	}

	// Fall back to RemoteAddr (direct connection IP)
	return remoteIP
}
