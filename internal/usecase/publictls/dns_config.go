package publictls

import (
	"fmt"
	"net"
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

// Validate checks DNSConfig for ACME DNS-01 usage.
func (c DNSConfig) Validate() error {
	if len(c.Resolvers) == 0 {
		return fmt.Errorf("dns.resolvers must contain at least one resolver")
	}
	for i, resolver := range c.Resolvers {
		resolver = strings.TrimSpace(resolver)
		if resolver == "" {
			return fmt.Errorf("invalid dns.resolvers[%d]: empty resolver", i)
		}
		if _, _, err := net.SplitHostPort(resolver); err != nil {
			return fmt.Errorf("invalid dns.resolvers[%d]: %q: expected host:port: %w", i, resolver, err)
		}
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
