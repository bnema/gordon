package publictls

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultDNSPropagationTimeout = 5 * time.Minute
	DefaultDNSPollingInterval    = 5 * time.Second
)

var DefaultDNSResolvers = []string{"1.1.1.1:53", "8.8.8.8:53"}

// DNSConfig controls public DNS visibility checks used by ACME DNS-01.
// These are recursive resolvers, not authoritative DNS providers.
type DNSConfig struct {
	Resolvers          []string
	PropagationTimeout time.Duration
	PollingInterval    time.Duration
}

// DefaultDNSConfig returns Gordon's default public DNS check configuration.
func DefaultDNSConfig() DNSConfig {
	resolvers := append([]string(nil), DefaultDNSResolvers...)
	return DNSConfig{
		Resolvers:          resolvers,
		PropagationTimeout: DefaultDNSPropagationTimeout,
		PollingInterval:    DefaultDNSPollingInterval,
	}
}

// Validate checks DNSConfig for ACME DNS-01 usage and normalizes resolver whitespace.
func (c *DNSConfig) Validate() error {
	if len(c.Resolvers) == 0 {
		return fmt.Errorf("dns.resolvers must contain at least one resolver")
	}
	for i, resolver := range c.Resolvers {
		trimmed := strings.TrimSpace(resolver)
		if trimmed == "" {
			return fmt.Errorf("invalid dns.resolvers[%d]: empty resolver", i)
		}
		host, port, err := net.SplitHostPort(trimmed)
		if err != nil {
			return fmt.Errorf("invalid dns.resolvers[%d]: %q: expected host:port: %w", i, trimmed, err)
		}
		if host == "" {
			return fmt.Errorf("invalid dns.resolvers[%d]: %q: host must not be empty", i, trimmed)
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return fmt.Errorf("invalid dns.resolvers[%d]: %q: port must be a number between 1 and 65535", i, trimmed)
		}
		c.Resolvers[i] = trimmed
	}
	if c.PropagationTimeout <= 0 {
		return fmt.Errorf("dns.propagation_timeout must be positive")
	}
	if c.PollingInterval <= 0 {
		return fmt.Errorf("dns.polling_interval must be positive")
	}
	if c.PollingInterval >= c.PropagationTimeout {
		return fmt.Errorf("dns.polling_interval must be less than dns.propagation_timeout")
	}
	return nil
}
