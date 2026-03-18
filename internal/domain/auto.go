package domain

import "time"

// AutoConfig holds the unified auto-route and auto-preview configuration.
type AutoConfig struct {
	Enabled        bool
	AllowedDomains []string
	Route          AutoRouteConfig
	Preview        PreviewConfig
}

// AutoRouteConfig holds auto-route specific settings (reserved for future use).
type AutoRouteConfig struct{}

// PreviewConfig holds auto-preview specific settings.
type PreviewConfig struct {
	Enabled     bool
	TTL         time.Duration
	Separator   string
	TagPatterns []string
	DataCopy    bool
	EnvCopy     bool
}
