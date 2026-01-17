package dto

// StatusResponse represents admin status information.
type StatusResponse struct {
	Routes            int               `json:"routes"`
	RegistryDomain    string            `json:"registry_domain"`
	RegistryPort      int               `json:"registry_port"`
	ServerPort        int               `json:"server_port"`
	AutoRoute         bool              `json:"auto_route"`
	NetworkIsolation  bool              `json:"network_isolation"`
	ContainerStatuses map[string]string `json:"container_status"`
}
