package domain

import (
	"fmt"
	"strings"
	"time"
)

// ProbeProtocol is the transport one bounded container-network readiness
// session uses.
type ProbeProtocol string

const (
	ProbeProtocolHTTP ProbeProtocol = "http"
	ProbeProtocolTCP  ProbeProtocol = "tcp"
)

// ContainerNetworkProbeRequest is one bounded readiness session against a
// declared internal container port. It carries exact identity: the target
// container, the execution start that must still be current when the
// session completes, and the already-derived private network the helper
// joins. Callers never pass a network they did not derive from the app
// incarnation.
type ContainerNetworkProbeRequest struct {
	// TargetContainerID is the exact container to probe.
	TargetContainerID string
	// ExpectedStartedAt is the execution start the adapter must observe on
	// the target before and after the session. A mismatch means the
	// generation changed under the probe and the result is discarded.
	ExpectedStartedAt time.Time
	// Network is the derived private network the helper joins. It is the
	// only network the helper is attached to.
	Network  string
	Protocol ProbeProtocol
	Port     int
	// Path is the origin-form request path for HTTP probes; empty for TCP.
	Path string
	// Timeout bounds the operational session through outcome validation.
	// Mandatory force-removal uses its own independent bounded context.
	Timeout time.Duration
}

// ContainerNetworkProbeResult reports one completed session.
type ContainerNetworkProbeResult struct {
	// Ready is true when the target accepted a 2xx/3xx HTTP status, or an
	// accepted TCP connection.
	Ready bool
	// Status is the observed HTTP status code when one was read, else 0.
	Status int
	// Diagnostic is a bounded, non-sensitive description of an unhealthy
	// attempt. It never carries addresses, paths, or raw command output.
	Diagnostic string
}

// Validate checks the request shape. It rejects missing identity or
// network, an invalid protocol, port, path, or timeout, and a path on a
// TCP probe.
func (r ContainerNetworkProbeRequest) Validate() error {
	if strings.TrimSpace(r.TargetContainerID) == "" {
		return fmt.Errorf("%w: target container id is required", ErrInvalidNetworkProbe)
	}
	if strings.TrimSpace(r.Network) == "" {
		return fmt.Errorf("%w: private network is required", ErrInvalidNetworkProbe)
	}
	if r.ExpectedStartedAt.IsZero() {
		// Without an execution boundary the probe could report readiness
		// for a generation that already restarted. Fail closed.
		return fmt.Errorf("%w: expected execution start is required", ErrInvalidNetworkProbe)
	}
	if r.Port < 1 || r.Port > 65535 {
		return fmt.Errorf("%w: port must be 1-65535", ErrInvalidNetworkProbe)
	}
	if r.Timeout <= 0 {
		return fmt.Errorf("%w: timeout must be positive", ErrInvalidNetworkProbe)
	}
	switch r.Protocol {
	case ProbeProtocolHTTP:
		if err := ValidateReadinessPath(r.Path); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidNetworkProbe, err)
		}
	case ProbeProtocolTCP:
		if r.Path != "" {
			return fmt.Errorf("%w: tcp probe takes no path", ErrInvalidNetworkProbe)
		}
	default:
		return fmt.Errorf("%w: protocol must be http|tcp", ErrInvalidNetworkProbe)
	}
	return nil
}
