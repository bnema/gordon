package dto

// Network represents a network in API responses.
type Network struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Containers []string          `json:"containers"`
	Labels     map[string]string `json:"labels"`
}

// NetworksResponse represents a list of networks.
type NetworksResponse struct {
	Networks []Network `json:"networks"`
}
