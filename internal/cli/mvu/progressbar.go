package mvu

import (
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	padding  = 2
	maxWidth = 80
)

var helpStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#626262")).Render

type ProgressMsg float64

type Model struct {
	progress progress.Model
	Done     bool
}

type ProgressReader struct {
	Reader     io.ReadCloser
	Total      int64
	ReadBytes  int64
	ProgressCh chan<- ProgressMsg
}

func NewPBModel() Model {
	m := progress.New(progress.WithGradient("#007BC0", "#011E5C"))
	m.Width = maxWidth

	return Model{
		progress: m,
	}
}

func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	if err != nil {
		return n, err
	}

	pr.ReadBytes += int64(n)
	percentage := float64(pr.ReadBytes) / float64(pr.Total)
	pr.ProgressCh <- ProgressMsg(percentage) // Send the percentage on the channel.
	return n, err
}

func (pr *ProgressReader) Close() error {
	if closer, ok := pr.Reader.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (m Model) Init() tea.Cmd {
	m.progress.Init()
	m.progress.SetPercent(0)
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m, tea.Quit

	case tea.WindowSizeMsg:
		m.progress.Width = msg.Width - padding*2 - 4
		if m.progress.Width > maxWidth {
			m.progress.Width = maxWidth
		}
		return m, nil

	case ProgressMsg:
		// Update the progress bar with the percentage received on the channel.
		cmd := m.progress.SetPercent(float64(msg))

		// If the percentage is 100, set Done to true.
		if msg == 1 {
			m.Done = true
			// quit
			return m, tea.Quit
		}

		return m, cmd

	case progress.FrameMsg:
		progressModel, cmd := m.progress.Update(msg)
		m.progress = progressModel.(progress.Model)
		return m, cmd

	default:
		return m, nil
	}
}

func (m Model) View() string {
	pad := strings.Repeat(" ", padding)
	return "\n" +
		pad + m.progress.View() + "\n\n" +
		pad + helpStyle("Press any key to quit")
}
