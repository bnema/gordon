package domain

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// EntryPointListener is the canonical public bind of one installation
// entrypoint. An app L4 publish declaration must be exactly compatible
// with the entrypoint that will actually bind it: the runtime binds the
// entrypoint address, so a narrower publish (for example loopback) would
// silently be exposed on the entrypoint listener instead.
type EntryPointListener struct {
	// Address is the configured listen address, "host:port" or ":port".
	Address string
	// Protocol is the entrypoint transport.
	Protocol EntryPointProtocol
}

// CanonicalBindHost normalizes a bind host: an empty host means "all
// interfaces" and is reported as 0.0.0.0 so an unset host and an explicit
// wildcard compare equal.
func CanonicalBindHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "0.0.0.0"
	}
	return host
}

// EntryPointCarriesTransport reports whether an entrypoint protocol can
// carry the given app interface transport.
func EntryPointCarriesTransport(protocol EntryPointProtocol, transport NetworkProtocol) bool {
	switch transport {
	case NetworkProtocolTCP:
		switch protocol {
		case EntryPointProtocolTCP, EntryPointProtocolTLSMux, EntryPointProtocolSmartTCP:
			return true
		}
		return false
	case NetworkProtocolUDP:
		return protocol == EntryPointProtocolUDP
	default:
		return false
	}
}

// ValidatePublishForListener requires a declared publish bind to equal the
// entrypoint listener address and the declared transport to be supported
// by the entrypoint protocol. Any mismatch is rejected before persistence
// or container effects; a caller must never substitute the entrypoint
// address for a narrower declared bind.
func ValidatePublishForListener(publish string, transport NetworkProtocol, listener EntryPointListener, entrypointName string) error {
	host, port, err := ParsePublish(publish)
	if err != nil {
		return err
	}
	listenerHost, listenerPort, err := SplitListenerAddress(listener.Address)
	if err != nil {
		return fmt.Errorf("%w: entrypoint %q listener address %q is invalid: %s", ErrInvalidAppSpec, entrypointName, listener.Address, err)
	}
	if port != listenerPort || CanonicalBindHost(host) != CanonicalBindHost(listenerHost) {
		return fmt.Errorf(
			"%w: publish %q for entrypoint %q must match the listener bind %q exactly",
			ErrInvalidAppSpec, publish, entrypointName, listener.Address,
		)
	}
	if !EntryPointCarriesTransport(listener.Protocol, transport) {
		return fmt.Errorf(
			"%w: entrypoint %q protocol %q cannot carry %s traffic",
			ErrInvalidAppSpec, entrypointName, listener.Protocol, transport,
		)
	}
	return nil
}

// SplitListenerAddress parses a configured listen address into its
// canonical host and port. An address without a port is invalid: the
// runtime always binds a concrete port.
func SplitListenerAddress(address string) (string, int, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", 0, fmt.Errorf("empty listen address")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("port %q must be 1-65535", portText)
	}
	return host, port, nil
}
