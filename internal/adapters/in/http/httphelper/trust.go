package httphelper

import (
	"net"
	"net/http"
	"strconv"
	"strings"
)

// LocalhostNets contains IPv4 and IPv6 loopback ranges.
var LocalhostNets = ParseTrustedProxies([]string{"127.0.0.0/8", "::1"})

// ParseTrustedProxies converts a list of IP addresses and CIDR ranges to [net.IPNet].
// It accepts both single IPs (e.g., "10.0.0.1") and CIDR notation (e.g., "10.0.0.0/8").
// Single IPs are converted to /32 (IPv4) or /128 (IPv6) CIDR blocks.
func ParseTrustedProxies(proxies []string) []*net.IPNet {
	var nets []*net.IPNet
	for _, proxy := range proxies {
		proxy = strings.TrimSpace(proxy)
		if proxy == "" {
			continue
		}

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

// IsTrustedProxy reports whether the given IP address falls within any of the
// trusted networks. Returns false if trustedNets is empty or the IP cannot be parsed.
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

// IsTrustedOrLocal reports whether an IP belongs to localhost or the given
// trusted proxy networks.
func IsTrustedOrLocal(ip string, proxyNets []*net.IPNet) bool {
	return IsTrustedProxy(ip, LocalhostNets) || IsTrustedProxy(ip, proxyNets)
}

// ExtractRemoteIP returns the IP portion of an [http.Request.RemoteAddr],
// stripping the port. When RemoteAddr lacks a port (e.g. Unix sockets or
// certain proxy setups), it is returned as-is.
func ExtractRemoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// IsTrustedSource reports whether the request's remote address is in trustedNets.
// When RemoteAddr lacks a port, IPv6 brackets are stripped before checking.
func IsTrustedSource(r *http.Request, trustedNets []*net.IPNet) bool {
	if len(trustedNets) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr may be a bare IP without a port (e.g. from Unix sockets
		// or certain proxy setups). Fall back to using it directly.
		host = strings.TrimSuffix(strings.TrimPrefix(r.RemoteAddr, "["), "]")
	}
	return IsTrustedProxy(host, trustedNets)
}

// HTTPSAuthority converts an incoming Host header plus httpPort/tlsPort into
// the correct HTTPS authority (host or host:port).
//
// Rules:
//   - No port in Host: omit TLS port (public reverse-proxy assumed on :443).
//   - Host port == httpPort: map to tlsPort.
//   - Any other explicit port: preserve it.
func HTTPSAuthority(host string, httpPort, tlsPort int) string {
	hostname, portStr, err := net.SplitHostPort(host)
	if err != nil {
		// No port in Host header — omit TLS port from URL.
		return host
	}

	port, err := strconv.Atoi(portStr)
	if err == nil && port == httpPort {
		return net.JoinHostPort(hostname, strconv.Itoa(tlsPort))
	}

	// Unknown explicit port — preserve it.
	return net.JoinHostPort(hostname, portStr)
}
