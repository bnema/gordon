package domain

import (
	"context"
	"time"
)

// EventType defines the type of event that occurred.
type EventType string

const (
	EventImagePushed    EventType = "image.pushed"
	EventImageDeleted   EventType = "image.deleted"
	EventConfigReload   EventType = "config.reload"
	EventSecretsChanged EventType = "secrets.changed"
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

// ConfigReloadPayload contains data for config.reload events.
type ConfigReloadPayload struct {
	Source        string // "file" or "manual"
	AddedRoutes   []string
	RemovedRoutes []string
	UpdatedRoutes []string
}

// SecretsChangedPayload contains data for secrets.changed events.
type SecretsChangedPayload struct {
	Domain    string   // Route domain whose secrets changed
	Operation string   // "set" or "delete"
	Keys      []string // Secret key names (not values)
}

// Context keys for domain-level concerns.
type contextKey string

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
