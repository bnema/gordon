// Package components provides reusable TUI components for Gordon's CLI.
package components

import (
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SpinnerModel wraps the bubbles spinner with Gordon styling.
type SpinnerModel struct {
	spinner spinner.Model
	message string
	style   lipgloss.Style
}

// SpinnerOption configures a SpinnerModel.
type SpinnerOption func(*SpinnerModel)

// NewSpinner creates a new spinner with Gordon styling.
func NewSpinner(opts ...SpinnerOption) SpinnerModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.ColorPrimary)

	m := SpinnerModel{
		spinner: s,
		message: "Loading...",
		style:   styles.Theme.Body,
	}

	for _, opt := range opts {
		opt(&m)
	}

	return m
}

// WithMessage sets the spinner message.
func WithMessage(msg string) SpinnerOption {
	return func(m *SpinnerModel) {
		m.message = msg
	}
}

// WithSpinnerType sets the spinner animation type.
func WithSpinnerType(t spinner.Spinner) SpinnerOption {
	return func(m *SpinnerModel) {
		m.spinner.Spinner = t
	}
}

// WithSpinnerColor sets the spinner color.
func WithSpinnerColor(color lipgloss.Color) SpinnerOption {
	return func(m *SpinnerModel) {
		m.spinner.Style = lipgloss.NewStyle().Foreground(color)
	}
}

// Init implements tea.Model.
func (m SpinnerModel) Init() tea.Cmd {
	return m.spinner.Tick
}

// Update implements tea.Model.
func (m SpinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
}

// View implements tea.Model.
func (m SpinnerModel) View() string {
	return m.spinner.View() + " " + m.style.Render(m.message)
}

// SetMessage updates the spinner message.
func (m *SpinnerModel) SetMessage(msg string) {
	m.message = msg
}

// Tick returns the spinner tick command for animation.
func (m SpinnerModel) Tick() tea.Cmd {
	return m.spinner.Tick
}

// Common spinner types with Gordon colors.
var (
	// SpinnerDot is the default dot spinner.
	SpinnerDot = spinner.Dot

	// SpinnerLine is a simple line spinner.
	SpinnerLine = spinner.Line

	// SpinnerMiniDot is a smaller dot spinner.
	SpinnerMiniDot = spinner.MiniDot

	// SpinnerJump is a jumping spinner.
	SpinnerJump = spinner.Jump

	// SpinnerPulse is a pulsing spinner.
	SpinnerPulse = spinner.Pulse

	// SpinnerPoints is a points spinner.
	SpinnerPoints = spinner.Points

	// SpinnerGlobe is a globe spinner.
	SpinnerGlobe = spinner.Globe

	// SpinnerMoon is a moon phase spinner.
	SpinnerMoon = spinner.Moon

	// SpinnerMonkey is a monkey spinner.
	SpinnerMonkey = spinner.Monkey
)
