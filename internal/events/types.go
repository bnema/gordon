package events

import (
	"time"
)

type EventType string

const (
	ImagePushed          EventType = "image.pushed"
	ImageDeleted         EventType = "image.deleted"
	ConfigReload         EventType = "config.reload"
	ManualReload         EventType = "manual.reload"
	ContainerStop        EventType = "container.stop"
	ContainerStart       EventType = "container.start"
	ContainerHealthCheck EventType = "container.health_check"
)

type Event struct {
	ID          string      `json:"id"`
	Type        EventType   `json:"type"`
	Timestamp   time.Time   `json:"timestamp"`
	ImageName   string      `json:"image_name,omitempty"`
	Tag         string      `json:"tag,omitempty"`
	Route       string      `json:"route,omitempty"`
	ContainerID string      `json:"container_id,omitempty"`
	Data        interface{} `json:"data,omitempty"`
}

type ImagePushedPayload struct {
	Name      string `json:"name"`
	Reference string `json:"reference"`
	Manifest  []byte `json:"-"`
}

type EventHandler interface {
	Handle(event Event) error
	CanHandle(eventType EventType) bool
}

type EventBus interface {
	Publish(eventType EventType, payload interface{}) error
	Subscribe(handler EventHandler) error
	Unsubscribe(handler EventHandler) error
	Start() error
	Stop() error
}
