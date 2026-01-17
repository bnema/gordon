package dto

// ConfigResponse represents server configuration.
type ConfigResponse struct {
	Server           ServerConfig           `json:"server"`
	AutoRoute        AutoRouteConfig        `json:"auto_route"`
	NetworkIsolation NetworkIsolationConfig `json:"network_isolation"`
	Routes           []Route                `json:"routes"`
	ExternalRoutes   []ExternalRoute        `json:"external_routes"`
}

// ServerConfig represents server config details.
type ServerConfig struct {
	Port           int    `json:"port"`
	RegistryPort   int    `json:"registry_port"`
	RegistryDomain string `json:"registry_domain"`
	DataDir        string `json:"data_dir"`
}

// AutoRouteConfig represents auto-route config details.
type AutoRouteConfig struct {
	Enabled bool `json:"enabled"`
}

// NetworkIsolationConfig represents network isolation settings.
type NetworkIsolationConfig struct {
	Enabled bool   `json:"enabled"`
	Prefix  string `json:"prefix"`
}

// ExternalRoute represents an external route config entry.
type ExternalRoute struct {
	Domain string `json:"domain"`
	Target string `json:"target"`
}
