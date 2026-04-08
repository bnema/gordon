package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/bnema/gordon/internal/adapters/in/http/httphelper"
)

// cloudflareNets contains Cloudflare's published IPv4 and IPv6 ranges.
// Cf-Connecting-Ip is only trusted when the immediate upstream matches these.
// Source: https://www.cloudflare.com/ips/
var cloudflareNets = httphelper.ParseTrustedProxies([]string{
	// IPv4
	"173.245.48.0/20",
	"103.21.244.0/22",
	"103.22.200.0/22",
	"103.31.4.0/22",
	"141.101.64.0/18",
	"108.162.192.0/18",
	"190.93.240.0/20",
	"188.114.96.0/20",
	"197.234.240.0/22",
	"198.41.128.0/17",
	"162.158.0.0/15",
	"104.16.0.0/13",
	"104.24.0.0/14",
	"172.64.0.0/13",
	"131.0.72.0/22",
	// IPv6
	"2400:cb00::/32",
	"2606:4700::/32",
	"2803:f800::/32",
	"2405:b500::/32",
	"2405:8100::/32",
	"2a06:98c0::/29",
	"2c0f:f248::/32",
})

func normalizeIP(raw string) string {
	ip := strings.TrimSpace(raw)
	if ip == "" {
		return ""
	}

	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}

	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}

	return parsed.String()
}

// ParseTrustedProxies converts a list of IP addresses and CIDR ranges to net.IPNet.
// It accepts both single IPs (e.g., "10.0.0.1") and CIDR notation (e.g., "10.0.0.0/8").
// Single IPs are converted to /32 (IPv4) or /128 (IPv6) CIDR blocks.
//
// Deprecated: use httphelper.ParseTrustedProxies directly. This wrapper preserves
// the existing middleware API for callers that have not migrated yet.
func ParseTrustedProxies(proxies []string) []*net.IPNet {
	return httphelper.ParseTrustedProxies(proxies)
}

// IsTrustedProxy checks if the given IP address is from a trusted proxy.
// Returns false if trustedNets is empty or the IP cannot be parsed.
//
// Deprecated: use httphelper.IsTrustedProxy directly. This wrapper preserves
// the existing middleware API for callers that have not migrated yet.
func IsTrustedProxy(ip string, trustedNets []*net.IPNet) bool {
	return httphelper.IsTrustedProxy(ip, trustedNets)
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
	if normalized := normalizeIP(remoteIP); normalized != "" {
		remoteIP = normalized
	}

	// Only honor proxy headers if the request comes from a trusted proxy
	if IsTrustedProxy(remoteIP, trustedNets) {
		// Cf-Connecting-IP is set by Cloudflare to the true client IP.
		// Only trust it when the immediate upstream is a known Cloudflare IP,
		// not any generic trusted proxy (e.g. local LB/nginx), because a
		// non-Cloudflare proxy would blindly forward a spoofed header.
		if IsTrustedProxy(remoteIP, cloudflareNets) {
			if cfIP := r.Header.Get("Cf-Connecting-Ip"); cfIP != "" {
				if ip := normalizeIP(cfIP); ip != "" {
					return ip
				}
			}
		}

		// Check X-Forwarded-For for proxied requests.
		// SECURITY: Parse from right to left and return the first untrusted IP.
		// This prevents spoofing when a trusted proxy appends to an attacker-
		// supplied XFF chain.
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			validChain := make([]string, 0, len(parts))
			for _, part := range parts {
				if ip := normalizeIP(part); ip != "" {
					validChain = append(validChain, ip)
				}
			}

			for i := len(validChain) - 1; i >= 0; i-- {
				if !IsTrustedProxy(validChain[i], trustedNets) {
					return validChain[i]
				}
			}

			if len(validChain) > 0 {
				return validChain[0]
			}
		}

		// Check X-Real-IP
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			if ip := normalizeIP(xri); ip != "" {
				return ip
			}
		}
	}

	// Fall back to RemoteAddr (direct connection IP)
	return remoteIP
}
