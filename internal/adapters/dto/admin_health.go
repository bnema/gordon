package dto

// HealthStatus represents health data for a route.
type HealthStatus struct {
	ContainerStatus string `json:"container_status"`
	HTTPStatus      int    `json:"http_status"`
	ResponseTimeMs  int64  `json:"response_time_ms"`
	Healthy         bool   `json:"healthy"`
	Error           string `json:"error"`
}

// HealthResponse represents the health status for all routes.
type HealthResponse struct {
	Health map[string]HealthStatus `json:"health"`
}
