package components

import (
	"fmt"
	"strings"

	"gordon/internal/adapters/in/cli/ui/styles"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// FieldType defines the type of form field.
type FieldType int

const (
	FieldTypeText FieldType = iota
	FieldTypePassword
	FieldTypeSelect
)

// FormField defines a form field configuration.
type FormField struct {
	Key         string
	Label       string
	Placeholder string
	Default     string
	Required    bool
	Type        FieldType
	Options     []string // For select fields
	Validator   func(string) error
}

// FormModel is an interactive form component.
type FormModel struct {
	title       string
	description string
	fields      []FormField
	inputs      []textinput.Model
	focused     int
	submitted   bool
	cancelled   bool
	errors      []string
	width       int

	// Styles
	titleStyle       lipgloss.Style
	descriptionStyle lipgloss.Style
	labelStyle       lipgloss.Style
	errorStyle       lipgloss.Style
}

// FormOption configures a FormModel.
type FormOption func(*FormModel)

// NewForm creates a new form with the given fields.
func NewForm(fields []FormField, opts ...FormOption) FormModel {
	inputs := make([]textinput.Model, len(fields))
	errors := make([]string, len(fields))

	for i, field := range fields {
		ti := textinput.New()
		ti.Placeholder = field.Placeholder
		ti.SetValue(field.Default)
		ti.CharLimit = 256
		ti.Width = 40

		if field.Type == FieldTypePassword {
			ti.EchoMode = textinput.EchoPassword
			ti.EchoCharacter = '*'
		}

		if i == 0 {
			ti.Focus()
		}

		inputs[i] = ti
	}

	m := FormModel{
		fields:           fields,
		inputs:           inputs,
		focused:          0,
		errors:           errors,
		width:            50,
		titleStyle:       styles.Theme.Title,
		descriptionStyle: styles.Theme.Muted,
		labelStyle:       styles.Theme.FormLabel,
		errorStyle:       styles.Theme.FormError,
	}

	for _, opt := range opts {
		opt(&m)
	}

	return m
}

// WithFormTitle sets the form title.
func WithFormTitle(title string) FormOption {
	return func(m *FormModel) {
		m.title = title
	}
}

// WithFormDescription sets the form description.
func WithFormDescription(desc string) FormOption {
	return func(m *FormModel) {
		m.description = desc
	}
}

// WithFormWidth sets the form width.
func WithFormWidth(w int) FormOption {
	return func(m *FormModel) {
		m.width = w
		for i := range m.inputs {
			m.inputs[i].Width = w - 4 // Account for padding
		}
	}
}

// Init implements tea.Model.
func (m FormModel) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model.
func (m FormModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "tab", "down":
			m.focusNext()
		case "shift+tab", "up":
			m.focusPrev()
		case "enter":
			if m.focused == len(m.fields)-1 {
				// On last field, submit
				if m.validate() {
					m.submitted = true
					return m, tea.Quit
				}
			} else {
				m.focusNext()
			}
		}
	}

	// Update the focused input
	var cmd tea.Cmd
	m.inputs[m.focused], cmd = m.inputs[m.focused].Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// focusNext moves focus to the next field.
func (m *FormModel) focusNext() {
	m.inputs[m.focused].Blur()
	m.focused = (m.focused + 1) % len(m.fields)
	m.inputs[m.focused].Focus()
}

// focusPrev moves focus to the previous field.
func (m *FormModel) focusPrev() {
	m.inputs[m.focused].Blur()
	m.focused--
	if m.focused < 0 {
		m.focused = len(m.fields) - 1
	}
	m.inputs[m.focused].Focus()
}

// validate validates all fields.
func (m *FormModel) validate() bool {
	valid := true
	for i, field := range m.fields {
		value := m.inputs[i].Value()
		m.errors[i] = ""

		// Check required
		if field.Required && strings.TrimSpace(value) == "" {
			m.errors[i] = "This field is required"
			valid = false
			continue
		}

		// Run custom validator
		if field.Validator != nil && value != "" {
			if err := field.Validator(value); err != nil {
				m.errors[i] = err.Error()
				valid = false
			}
		}
	}
	return valid
}

// View implements tea.Model.
func (m FormModel) View() string {
	var b strings.Builder

	// Title
	if m.title != "" {
		b.WriteString(m.titleStyle.Render(m.title))
		b.WriteString("\n")
	}

	// Description
	if m.description != "" {
		b.WriteString(m.descriptionStyle.Render(m.description))
		b.WriteString("\n")
	}

	if m.title != "" || m.description != "" {
		b.WriteString("\n")
	}

	// Fields
	for i, field := range m.fields {
		// Label
		label := field.Label
		if field.Required {
			label += " *"
		}
		b.WriteString(m.labelStyle.Render(label))
		b.WriteString("\n")

		// Input
		b.WriteString(m.inputs[i].View())
		b.WriteString("\n")

		// Error
		if m.errors[i] != "" {
			b.WriteString(m.errorStyle.Render(styles.IconError + " " + m.errors[i]))
			b.WriteString("\n")
		}

		b.WriteString("\n")
	}

	// Help
	help := styles.RenderKeyHelp("tab/shift+tab", "navigate") + "  " +
		styles.RenderKeyHelp("enter", "submit") + "  " +
		styles.RenderKeyHelp("esc", "cancel")
	b.WriteString(help)

	return b.String()
}

// Values returns the form values as a map.
func (m FormModel) Values() map[string]string {
	values := make(map[string]string)
	for i, field := range m.fields {
		values[field.Key] = m.inputs[i].Value()
	}
	return values
}

// Value returns the value for a specific field.
func (m FormModel) Value(key string) string {
	for i, field := range m.fields {
		if field.Key == key {
			return m.inputs[i].Value()
		}
	}
	return ""
}

// Submitted returns true if the form was submitted.
func (m FormModel) Submitted() bool {
	return m.submitted
}

// Cancelled returns true if the form was cancelled.
func (m FormModel) Cancelled() bool {
	return m.cancelled
}

// RunForm runs a form and returns the values.
// This is a convenience function for simple forms.
func RunForm(title string, fields []FormField) (map[string]string, error) {
	m := NewForm(fields, WithFormTitle(title))
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("error running form: %w", err)
	}
	result := finalModel.(FormModel)
	if result.Cancelled() {
		return nil, nil
	}
	if !result.Submitted() {
		return nil, nil
	}
	return result.Values(), nil
}

// TextField creates a text field configuration.
func TextField(key, label string, opts ...func(*FormField)) FormField {
	f := FormField{
		Key:   key,
		Label: label,
		Type:  FieldTypeText,
	}
	for _, opt := range opts {
		opt(&f)
	}
	return f
}

// PasswordField creates a password field configuration.
func PasswordField(key, label string, opts ...func(*FormField)) FormField {
	f := FormField{
		Key:   key,
		Label: label,
		Type:  FieldTypePassword,
	}
	for _, opt := range opts {
		opt(&f)
	}
	return f
}

// Required marks a field as required.
func Required() func(*FormField) {
	return func(f *FormField) {
		f.Required = true
	}
}

// WithPlaceholder sets the field placeholder.
func WithPlaceholder(p string) func(*FormField) {
	return func(f *FormField) {
		f.Placeholder = p
	}
}

// WithDefault sets the field default value.
func WithDefault(d string) func(*FormField) {
	return func(f *FormField) {
		f.Default = d
	}
}

// WithValidator sets the field validator.
func WithValidator(v func(string) error) func(*FormField) {
	return func(f *FormField) {
		f.Validator = v
	}
}
