package domain

// Route represents a mapping from a domain to a container image.
type Route struct {
	Domain string
	Image  string
	HTTPS  bool
}

// ProxyTarget represents the destination for proxying requests.
type ProxyTarget struct {
	Host        string
	Port        int
	ContainerID string
	Scheme      string // "http" or "https"
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
