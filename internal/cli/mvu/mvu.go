package mvu

import (
	"fmt"
	"net/http"

	tea "github.com/charmbracelet/bubbletea"
)

type Model struct {
	status int
	err    error
	url    string
}

type statusMsg int

type ErrMsg struct {
	Err error
}

func (m Model) Init() tea.Cmd {
	return nil
}

func Error(e ErrMsg) string { return e.Err.Error() }

func Update(msg tea.Msg, m Model) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		default:
			return nil
		}

	case statusMsg:
		m.status = int(msg)
		return m, tea.Quit

	case ErrMsg:
		m.err = msg
		return nil

	default:
		return nil
	}
}

func (m Model) View() string {
	s := fmt.Sprintf("Checking %s...", m.url)
	if m.err != nil {
		s += fmt.Sprintf("something went wrong: %s", m.err)
	} else if m.status != 0 {
		s += fmt.Sprintf("%d %s", m.status, http.StatusText(m.status))
	}
	return s + "\n"
}
