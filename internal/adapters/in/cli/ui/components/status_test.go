package components

import (
	"os"
	"testing"

	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	// Force TrueColor so lipgloss renders ANSI color codes in tests
	// (without a TTY, lipgloss strips all styling).
	lipgloss.SetColorProfile(termenv.TrueColor)
	os.Exit(m.Run())
}

func TestStatusIcon_ReturnsStyledIcon(t *testing.T) {
	tests := []struct {
		name   string
		icon   string
		status Status
	}{
		{"success renders without error", styles.IconContainerStatus, StatusSuccess},
		{"running renders without error", styles.IconContainerStatus, StatusRunning},
		{"error renders without error", styles.IconHTTPStatus, StatusError},
		{"stopped renders without error", styles.IconContainerStatus, StatusStopped},
		{"exited renders without error", styles.IconContainerStatus, StatusExited},
		{"unknown renders without error", styles.IconHTTPStatus, StatusUnknown},
		{"pending renders without error", styles.IconHTTPStatus, StatusPending},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StatusIcon(tt.icon, tt.status)
			assert.NotEmpty(t, result)
			assert.Contains(t, result, tt.icon)
		})
	}
}

func TestStatusIcon_DifferentStatusesProduceDifferentOutput(t *testing.T) {
	success := StatusIcon(styles.IconContainerStatus, StatusRunning)
	failure := StatusIcon(styles.IconContainerStatus, StatusError)
	unknown := StatusIcon(styles.IconContainerStatus, StatusUnknown)

	assert.NotEqual(t, success, failure)
	assert.NotEqual(t, success, unknown)
	assert.NotEqual(t, failure, unknown)
}
