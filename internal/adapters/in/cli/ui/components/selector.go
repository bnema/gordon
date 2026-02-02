package components

import (
	"fmt"
	"strings"

	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SelectorModel is a list selection dialog.
type SelectorModel struct {
	title     string
	items     []string
	current   string // highlighted item (e.g., current tag)
	cursor    int
	selected  string
	cancelled bool
}

// NewSelector creates a new selector dialog.
func NewSelector(title string, items []string, current string) SelectorModel {
	return SelectorModel{
		title:   title,
		items:   items,
		current: current,
	}
}

func (m SelectorModel) Init() tea.Cmd { return nil }

func (m SelectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if len(m.items) == 0 {
			switch msg.String() {
			case "esc", "ctrl+c", "q":
				m.cancelled = true
				return m, tea.Quit
			}
			return m, nil
		}
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "enter":
			m.selected = m.items[m.cursor]
			return m, tea.Quit
		case "esc", "ctrl+c", "q":
			m.cancelled = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m SelectorModel) View() string {
	var b strings.Builder

	b.WriteString(styles.Theme.Bold.Render(m.title))
	b.WriteString("\n\n")

	for i, item := range m.items {
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}

		line := item
		if item == m.current {
			line = item + " " + lipgloss.NewStyle().Foreground(styles.ColorPrimary).Render("(current)")
		}

		if i == m.cursor {
			b.WriteString(styles.Theme.Highlight.Render(cursor + line))
		} else {
			b.WriteString(fmt.Sprintf("%s%s", cursor, line))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(styles.RenderKeyHelp("↑/↓", "navigate") + "  " +
		styles.RenderKeyHelp("enter", "select") + "  " +
		styles.RenderKeyHelp("esc", "cancel"))

	return b.String()
}

func (m SelectorModel) Selected() string { return m.selected }
func (m SelectorModel) Cancelled() bool  { return m.cancelled }

// RunSelector runs a selector dialog and returns the selected item.
func RunSelector(title string, items []string, current string) (string, error) {
	m := NewSelector(title, items, current)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return "", fmt.Errorf("error running selector: %w", err)
	}
	result := finalModel.(SelectorModel)
	if result.Cancelled() {
		return "", nil
	}
	return result.Selected(), nil
}
