package components

import (
	"fmt"
	"strings"

	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ConfirmResult represents the result of a confirmation dialog.
type ConfirmResult int

const (
	ConfirmPending ConfirmResult = iota
	ConfirmYes
	ConfirmNo
	ConfirmCancelled
)

// ConfirmModel is a Yes/No confirmation dialog.
type ConfirmModel struct {
	question    string
	description string
	yesLabel    string
	noLabel     string
	focused     bool // true = Yes is focused, false = No is focused
	result      ConfirmResult
	width       int

	// Styles
	questionStyle    lipgloss.Style
	descriptionStyle lipgloss.Style
	buttonStyle      lipgloss.Style
	focusedStyle     lipgloss.Style
}

// ConfirmOption configures a ConfirmModel.
type ConfirmOption func(*ConfirmModel)

// NewConfirm creates a new confirmation dialog.
func NewConfirm(question string, opts ...ConfirmOption) ConfirmModel {
	m := ConfirmModel{
		question:         question,
		yesLabel:         "Yes",
		noLabel:          "No",
		focused:          false, // Default to No
		result:           ConfirmPending,
		width:            50,
		questionStyle:    styles.Theme.Bold,
		descriptionStyle: styles.Theme.Muted,
		buttonStyle: lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(styles.ColorText),
		focusedStyle: lipgloss.NewStyle().
			Padding(0, 2).
			Bold(true).
			Foreground(styles.ColorBg).
			Background(styles.ColorPrimary),
	}

	for _, opt := range opts {
		opt(&m)
	}

	return m
}

// WithDescription adds a description to the confirmation.
func WithDescription(desc string) ConfirmOption {
	return func(m *ConfirmModel) {
		m.description = desc
	}
}

// WithDefaultYes sets Yes as the default selection.
func WithDefaultYes() ConfirmOption {
	return func(m *ConfirmModel) {
		m.focused = true
	}
}

// WithLabels customizes the Yes/No labels.
func WithLabels(yes, no string) ConfirmOption {
	return func(m *ConfirmModel) {
		m.yesLabel = yes
		m.noLabel = no
	}
}

// WithWidth sets the dialog width.
func WithWidth(w int) ConfirmOption {
	return func(m *ConfirmModel) {
		m.width = w
	}
}

// Init implements tea.Model.
func (m ConfirmModel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m ConfirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "left", "h", "tab", "shift+tab":
			m.focused = !m.focused
		case "right", "l":
			m.focused = !m.focused
		case "y", "Y":
			m.result = ConfirmYes
			return m, tea.Quit
		case "n", "N":
			m.result = ConfirmNo
			return m, tea.Quit
		case "enter":
			if m.focused {
				m.result = ConfirmYes
			} else {
				m.result = ConfirmNo
			}
			return m, tea.Quit
		case "esc", "ctrl+c", "q":
			m.result = ConfirmCancelled
			return m, tea.Quit
		}
	}
	return m, nil
}

// View implements tea.Model.
func (m ConfirmModel) View() string {
	var b strings.Builder

	// Question
	b.WriteString(m.questionStyle.Render(m.question))
	b.WriteString("\n")

	// Description
	if m.description != "" {
		b.WriteString(m.descriptionStyle.Render(m.description))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Buttons
	var yesButton, noButton string
	if m.focused {
		yesButton = m.focusedStyle.Render(m.yesLabel)
		noButton = m.buttonStyle.Render(m.noLabel)
	} else {
		yesButton = m.buttonStyle.Render(m.yesLabel)
		noButton = m.focusedStyle.Render(m.noLabel)
	}

	buttons := lipgloss.JoinHorizontal(lipgloss.Center, yesButton, "  ", noButton)
	b.WriteString(buttons)
	b.WriteString("\n\n")

	// Help
	help := styles.RenderKeyHelp("y/n", "select") + "  " +
		styles.RenderKeyHelp("enter", "confirm") + "  " +
		styles.RenderKeyHelp("esc", "cancel")
	b.WriteString(help)

	return b.String()
}

// Result returns the confirmation result.
func (m ConfirmModel) Result() ConfirmResult {
	return m.result
}

// Confirmed returns true if the user confirmed.
func (m ConfirmModel) Confirmed() bool {
	return m.result == ConfirmYes
}

// Cancelled returns true if the user cancelled.
func (m ConfirmModel) Cancelled() bool {
	return m.result == ConfirmCancelled
}

// RunConfirm runs a confirmation dialog and returns the result.
// This is a convenience function for simple confirmations.
func RunConfirm(question string, opts ...ConfirmOption) (bool, error) {
	m := NewConfirm(question, opts...)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return false, fmt.Errorf("error running confirmation: %w", err)
	}
	result := finalModel.(ConfirmModel)
	if result.Cancelled() {
		return false, nil
	}
	return result.Confirmed(), nil
}
