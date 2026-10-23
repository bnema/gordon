package oidc

import (
	"net"
	"net/netip"
)

// nonPublicPrefixes lists ranges always refused, beyond the netip predicates.
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // "this" network
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved, broadcast
	netip.MustParsePrefix("::/96"),         // IPv4-compatible (deprecated)
	netip.MustParsePrefix("fec0::/10"),     // site-local (deprecated)
	netip.MustParsePrefix("2001::/32"),     // Teredo tunnels to arbitrary IPv4
}

// Prefixes that embed an IPv4 address, which is checked instead.
var (
	nat64Prefixes = []netip.Prefix{
		netip.MustParsePrefix("64:ff9b::/96"),   // NAT64 well-known
		netip.MustParsePrefix("64:ff9b:1::/48"), // NAT64 local-use
	}
	sixToFourPrefix = netip.MustParsePrefix("2002::/16")
)

// isPublicIP reports whether ip is a globally routable unicast address. It
// rejects loopback, link-local (incl. 169.254.169.254), RFC 1918, 100.64/10,
// ULA, unspecified, multicast, other reserved ranges and Teredo. NAT64 and
// 6to4 addresses are judged by the IPv4 address they embed.
func isPublicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	return isPublicAddr(addr.Unmap())
}

func isPublicAddr(addr netip.Addr) bool {
	if embedded, ok := embeddedIPv4(addr); ok {
		return isPublicAddr(embedded)
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// embeddedIPv4 returns the IPv4 address carried by a NAT64 (last 32 bits) or
// 6to4 (bits 16-48) address.
func embeddedIPv4(addr netip.Addr) (netip.Addr, bool) {
	b := addr.As16()
	for _, prefix := range nat64Prefixes {
		if prefix.Contains(addr) {
			return netip.AddrFrom4([4]byte(b[12:16])), true
		}
	}
	if sixToFourPrefix.Contains(addr) {
		return netip.AddrFrom4([4]byte(b[2:6])), true
	}
	return netip.Addr{}, false
}
