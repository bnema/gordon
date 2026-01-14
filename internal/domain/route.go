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
