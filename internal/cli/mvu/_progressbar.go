package mvu

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	padding  = 2
	maxWidth = 80
)

type progressMsg int64

type Model struct {
	progress   progress.Model
	actualSize int64
}

var helpStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#626262")).Render

// Update your model to listen to the progress channel.
func NewProgressBarModel(actualSize int64, progressCh <-chan int64) Model {
	m := Model{
		progress:   progress.New(progress.WithDefaultGradient()),
		actualSize: actualSize,
	}

	// Start the program and listen to the channel in a separate goroutine
	go func() {
		p := tea.NewProgram(m)
		for n := range progressCh {
			percent := float64(n) / float64(m.actualSize)
			p.Send(progressMsg(int64(percent * 100))) // Send update as percent
		}
	}()

	return m
}

func (m Model) Init() tea.Cmd {
	// Start the channel listener
	m.progress.SetPercent(100)
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

	case progressMsg:
		percent := float64(msg) / 100.0 // Convert percentage back to a float
		m.progress.SetPercent(percent)
		fmt.Printf("Progress message received. Percent: %.2f%%\n", percent*100)
		if percent >= 1.0 {
			return m, tea.Quit
		}
		return m, nil

	// FrameMsg is sent when the progress bar wants to animate itself
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

// p.Send(mvu.ProgressMsg(int64(percent * 100))) // Send update as percent

func (m Model) Send(msg progressMsg) {
	m.progress.Update(msg)
}

func ProgressMsg(percent int64) progressMsg {
	return progressMsg(percent)
}
