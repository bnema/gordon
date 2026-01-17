// Package dto provides shared data transfer objects for API responses.
package dto

// Route represents route configuration in API responses.
type Route struct {
	Domain string `json:"domain"`
	Image  string `json:"image"`
	HTTPS  bool   `json:"https"`
}

// RouteInfo represents route details in API responses.
type RouteInfo struct {
	Domain          string       `json:"domain"`
	Image           string       `json:"image"`
	ContainerID     string       `json:"container_id"`
	ContainerStatus string       `json:"container_status"`
	Network         string       `json:"network"`
	Attachments     []Attachment `json:"attachments"`
}

// Attachment represents an attached service in API responses.
type Attachment struct {
	Name        string `json:"name"`
	Image       string `json:"image"`
	ContainerID string `json:"container_id"`
	Status      string `json:"status"`
}

// RoutesResponse represents a list of routes.
type RoutesResponse struct {
	Routes []Route `json:"routes"`
}

// RoutesDetailResponse represents a detailed list of routes.
type RoutesDetailResponse struct {
	Routes []RouteInfo `json:"routes"`
}

// AttachmentsResponse represents a list of attachments for a route.
type AttachmentsResponse struct {
	Attachments []Attachment `json:"attachments"`
}

// RouteDeleteResponse represents a route removal response.
type RouteDeleteResponse struct {
	Status string `json:"status"`
}
