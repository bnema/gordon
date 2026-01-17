// Package dto provides shared data transfer objects for API responses.
package dto

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
