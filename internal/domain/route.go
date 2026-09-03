package domain

// Route binds an HTTP hostname to a named port on a Gordon-owned service.
type Route struct {
	Domain   string
	Service  string
	PortName string
	Image    string // Removed public model; retained temporarily while route-owned deployment code is migrated.
	HTTPS    bool
	Env      []string // Pre-resolved env vars ("KEY=VALUE"); when set, Deploy skips EnvLoader lookup.
}

// HTTPServiceRoute maps a hostname to a named port on a Gordon-owned service.
// It binds HTTP traffic but does not own a separate route container.
type HTTPServiceRoute struct {
	Domain   string
	Service  string
	PortName string
	HTTPS    bool
}

// ProxyTarget represents the destination for proxying requests.
type ProxyTarget struct {
	Host         string
	Port         int
	ContainerID  string
	Scheme       string   // "http" or "https"
	Protocol     string   // "" (default HTTP/1.1) or "h2c" (cleartext HTTP/2)
	OriginalHost string   // Original hostname before DNS resolution (for external-route Host header)
	RouteHost    string   // Canonical matched route domain for managed-route Host header
	TrustedCIDRs []string // Direct peers allowed to access a private service port
}

// RouteMatch represents the result of matching a request to a route.
type RouteMatch struct {
	Route     Route
	Container *Container
	Target    *ProxyTarget
}

// ExternalRoute represents a mapping from a domain to an external (non-container) service.
type ExternalRoute struct {
	Domain string // e.g., "reg.bnema.dev"
	Host   string // e.g., "localhost"
	Port   int    // e.g., 5000
}

// RouteHealth represents the health status of a route.
type RouteHealth struct {
	Domain          string // The route domain
	ContainerStatus string // Container state: "running", "stopped", etc.
	HTTPStatus      int    // HTTP status code from probe (0 if unreachable)
	ResponseTimeMs  int64  // Response time in milliseconds
	Healthy         bool   // True if container running AND HTTP 2xx/3xx
	Error           string // Error message if probe failed
}
