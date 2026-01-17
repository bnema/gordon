package dto

// DeployResponse represents a deployment response.
type DeployResponse struct {
	Status      string `json:"status"`
	ContainerID string `json:"container_id"`
	Domain      string `json:"domain"`
}
