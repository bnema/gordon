package dto

// StatusResponse represents admin status information.
type StatusResponse struct {
	Apps              int               `json:"apps"`
	RegistryDomain    string            `json:"registry_domain"`
	RegistryPort      int               `json:"registry_port"`
	ServerPort        int               `json:"server_port"`
	NetworkIsolation  bool              `json:"network_isolation"`
	ContainerStatuses map[string]string `json:"container_status"`
}
