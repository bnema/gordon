package domain

import "time"

// EventType defines the type of event that occurred.
type EventType string

const (
	EventImagePushed          EventType = "image.pushed"
	EventImageDeleted         EventType = "image.deleted"
	EventConfigReload         EventType = "config.reload"
	EventManualReload         EventType = "manual.reload"
	EventContainerStop        EventType = "container.stop"
	EventContainerStart       EventType = "container.start"
	EventContainerHealthCheck EventType = "container.health_check"
)

// Event represents a domain event that occurred in the system.
type Event struct {
	ID          string
	Type        EventType
	Timestamp   time.Time
	ImageName   string
	Tag         string
	Route       string
	ContainerID string
	Data        any
}

// ImagePushedPayload contains data for image.pushed events.
type ImagePushedPayload struct {
	Name        string
	Reference   string
	Manifest    []byte
	Annotations map[string]string
}

// ContainerEventPayload contains data for container events.
type ContainerEventPayload struct {
	ContainerID string
	Domain      string
	Image       string
	Action      string
}

// ConfigReloadPayload contains data for config.reload events.
type ConfigReloadPayload struct {
	Source        string // "file" or "manual"
	AddedRoutes   []string
	RemovedRoutes []string
	UpdatedRoutes []string
}
