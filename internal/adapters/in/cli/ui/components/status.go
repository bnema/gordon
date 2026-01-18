package components

import (
	"strings"

	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"

	"github.com/charmbracelet/lipgloss"
)

// Status represents a status type for rendering.
type Status int

const (
	StatusSuccess Status = iota
	StatusError
	StatusWarning
	StatusInfo
	StatusPending
	StatusRunning
	StatusStopped
	StatusPaused
	StatusRestarting
	StatusExited
	StatusUnknown
)

// StatusConfig holds configuration for status rendering.
type StatusConfig struct {
	Status  Status
	Label   string
	Icon    string
	Style   lipgloss.Style
	Badge   bool // If true, render as a badge with background
	Compact bool // If true, render icon only
}

// DefaultStatusConfigs provides default configurations for each status type.
var DefaultStatusConfigs = map[Status]StatusConfig{
	StatusSuccess: {
		Icon:  styles.IconSuccess,
		Style: styles.Theme.Success,
	},
	StatusError: {
		Icon:  styles.IconError,
		Style: styles.Theme.Error,
	},
	StatusWarning: {
		Icon:  styles.IconWarning,
		Style: styles.Theme.Warning,
	},
	StatusInfo: {
		Icon:  styles.IconInfo,
		Style: styles.Theme.Info,
	},
	StatusPending: {
		Icon:  styles.IconPending,
		Style: styles.Theme.Muted,
	},
	StatusRunning: {
		Icon:  styles.IconRunning,
		Style: styles.Theme.Success,
	},
	StatusStopped: {
		Icon:  styles.IconStopped,
		Style: styles.Theme.Error,
	},
	StatusPaused: {
		Icon:  styles.IconPaused,
		Style: styles.Theme.Warning,
	},
	StatusRestarting: {
		Icon:  styles.IconRestarting,
		Style: styles.Theme.Warning,
	},
	StatusExited: {
		Icon:  styles.IconExited,
		Style: styles.Theme.Error,
	},
	StatusUnknown: {
		Icon:  styles.IconUnknown,
		Style: styles.Theme.Muted,
	},
}

// RenderStatus renders a status with icon and optional label.
func RenderStatus(status Status, label string) string {
	config := DefaultStatusConfigs[status]
	if label == "" {
		return config.Style.Render(config.Icon)
	}
	return config.Style.Render(config.Icon + " " + label)
}

// RenderStatusBadge renders a status as a compact icon badge with background.
// It displays only the icon (no label) for minimal width in table columns.
func RenderStatusBadge(status Status) string {
	config := DefaultStatusConfigs[status]
	var badgeStyle lipgloss.Style
	switch status {
	case StatusSuccess, StatusRunning:
		badgeStyle = styles.Theme.BadgeSuccess
	case StatusError, StatusStopped, StatusExited:
		badgeStyle = styles.Theme.BadgeError
	case StatusWarning, StatusPaused, StatusRestarting:
		badgeStyle = styles.Theme.BadgeWarning
	case StatusPending:
		badgeStyle = styles.Theme.BadgePending
	case StatusUnknown:
		badgeStyle = styles.Theme.Muted
	default:
		badgeStyle = styles.Theme.BadgeInfo
	}
	return badgeStyle.Render(config.Icon)
}

// ParseStatus converts a string status to Status type.
func ParseStatus(s string) Status {
	switch strings.ToLower(s) {
	case "success", "ok", "active", "healthy":
		return StatusSuccess
	case "error", "failed", "unhealthy":
		return StatusError
	case "warning", "degraded":
		return StatusWarning
	case "info":
		return StatusInfo
	case "pending", "starting", "waiting", "created":
		return StatusPending
	case "running", "up":
		return StatusRunning
	case "stopped", "down":
		return StatusStopped
	case "paused":
		return StatusPaused
	case "restarting":
		return StatusRestarting
	case "exited", "dead":
		return StatusExited
	case "unknown":
		return StatusUnknown
	default:
		return StatusUnknown
	}
}

// StatusIndicator is a simple status indicator component.
type StatusIndicator struct {
	Status Status
	Label  string
	Badge  bool
}

// NewStatusIndicator creates a new status indicator.
func NewStatusIndicator(status Status, label string) StatusIndicator {
	return StatusIndicator{
		Status: status,
		Label:  label,
		Badge:  false,
	}
}

// NewStatusBadge creates a new status badge (with background).
func NewStatusBadge(status Status, label string) StatusIndicator {
	return StatusIndicator{
		Status: status,
		Label:  label,
		Badge:  true,
	}
}

// View renders the status indicator.
func (s StatusIndicator) View() string {
	if s.Badge {
		return RenderStatusBadge(s.Status)
	}
	return RenderStatus(s.Status, s.Label)
}

// ContainerStatusIndicator renders container status with appropriate styling.
func ContainerStatusIndicator(status string) string {
	parsed := ParseStatus(status)
	return RenderStatus(parsed, status)
}

// ContainerStatusBadge renders container status as a badge.
func ContainerStatusBadge(status string) string {
	parsed := ParseStatus(status)
	return RenderStatusBadge(parsed)
}
