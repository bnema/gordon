package dto

// ConfigResponse represents server configuration.
// ConfigResponse represents installation configuration. App routes live
// under apps show, never here.
type ConfigResponse struct {
	Server           ServerConfig           `json:"server"`
	NetworkIsolation NetworkIsolationConfig `json:"network_isolation"`
	Volumes          VolumesConfig          `json:"volumes"`
	ExternalRoutes   []ExternalRoute        `json:"external_routes"`
}

// ServerConfig represents server config details.
type ServerConfig struct {
	Port           int    `json:"port"`
	RegistryPort   int    `json:"registry_port"`
	RegistryDomain string `json:"registry_domain"`
	DataDir        string `json:"data_dir,omitempty"`
}

// NetworkIsolationConfig represents network isolation settings.
type NetworkIsolationConfig struct {
	Enabled bool   `json:"enabled"`
	Prefix  string `json:"prefix"`
}

// VolumesConfig represents volume settings relevant to runtime-managed volumes.
type VolumesConfig struct {
	AutoCreate bool   `json:"auto_create"`
	Prefix     string `json:"prefix"`
	Preserve   bool   `json:"preserve"`
}

// ExternalRoute represents an external route config entry.
type ExternalRoute struct {
	Domain string `json:"domain"`
	Target string `json:"target,omitempty"`
}
