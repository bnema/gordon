package domain

import (
	"context"
	"time"
)

// EventType defines the type of event that occurred.
type EventType string

const (
	EventImagePushed          EventType = "image.pushed"
	EventImageDeleted         EventType = "image.deleted"
	EventConfigReload         EventType = "config.reload"
	EventManualReload         EventType = "manual.reload"
	EventManualDeploy         EventType = "manual.deploy"
	EventContainerStop        EventType = "container.stop"
	EventContainerStart       EventType = "container.start"
	EventContainerHealthCheck EventType = "container.health_check"
	EventContainerDeployed    EventType = "container.deployed"
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

// ManualDeployPayload contains data for manual.deploy events.
type ManualDeployPayload struct {
	Domain string `json:"domain"`
}

// Context keys for domain-level concerns.
type contextKey string

const (
	// ContextKeyInternalDeploy indicates the deployment is triggered internally
	// (e.g., from our own registry's image.pushed event) and should use
	// internal registry authentication for image pulls.
	ContextKeyInternalDeploy contextKey = "internal_deploy"
)

// IsInternalDeploy checks if the context indicates an internal deployment.
func IsInternalDeploy(ctx context.Context) bool {
	v, ok := ctx.Value(ContextKeyInternalDeploy).(bool)
	return ok && v
}

// WithInternalDeploy returns a context marked as an internal deployment.
func WithInternalDeploy(ctx context.Context) context.Context {
	return context.WithValue(ctx, ContextKeyInternalDeploy, true)
}

const (
	// ContextKeySkipReadiness indicates that readiness checks should be skipped
	// (e.g., during AutoStart where the background monitor handles crash recovery).
	ContextKeySkipReadiness contextKey = "skip_readiness"
)

// WithSkipReadiness returns a context that skips readiness checks on deploy.
func WithSkipReadiness(ctx context.Context) context.Context {
	return context.WithValue(ctx, ContextKeySkipReadiness, true)
}

// IsSkipReadiness checks if the context indicates readiness should be skipped.
func IsSkipReadiness(ctx context.Context) bool {
	v, ok := ctx.Value(ContextKeySkipReadiness).(bool)
	return ok && v
}
