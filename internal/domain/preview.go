package domain

import "time"

// PreviewStatus represents the lifecycle state of a preview environment.
type PreviewStatus string

const (
	PreviewStatusDeploying PreviewStatus = "deploying"
	PreviewStatusRunning   PreviewStatus = "running"
	PreviewStatusFailed    PreviewStatus = "failed"
)

// PreviewRoute represents an ephemeral preview environment.
type PreviewRoute struct {
	Domain     string        `json:"domain"`
	Image      string        `json:"image"`
	BaseRoute  string        `json:"base_route"`
	Name       string        `json:"name"`
	CreatedAt  time.Time     `json:"created_at"`
	ExpiresAt  time.Time     `json:"expires_at"`
	HTTPS      bool          `json:"https"`
	Status     PreviewStatus `json:"status"`
	Volumes    []string      `json:"volumes"`
	Containers []string      `json:"containers"`
}

// IsExpired returns true if the preview has exceeded its TTL.
func (p PreviewRoute) IsExpired(now time.Time) bool {
	return now.After(p.ExpiresAt)
}
