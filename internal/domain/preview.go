package domain

import "time"

// PreviewRoute represents an ephemeral preview environment.
type PreviewRoute struct {
	Domain     string    `json:"domain"`
	Image      string    `json:"image"`
	BaseRoute  string    `json:"base_route"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	HTTPS      bool      `json:"https"`
	Volumes    []string  `json:"volumes"`
	Containers []string  `json:"containers"`
}

// IsExpired returns true if the preview has exceeded its TTL.
func (p PreviewRoute) IsExpired() bool {
	return time.Now().After(p.ExpiresAt)
}
