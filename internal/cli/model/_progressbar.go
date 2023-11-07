package model

import (
	"io"
	"log"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	padding  = 2
	maxWidth = 80
)

type progressMsg float64

type ProgressBarModel struct {
	Width        int
	progress     progress.Model
	progressChan chan float64
}

func NewProgressBarModel() ProgressBarModel {
	progressBar := progress.New(progress.WithScaledGradient("#FF7CCB", "#FDFF8C"))
	progressChan := make(chan float64)
	return ProgressBarModel{
		progress:     progressBar,
		progressChan: progressChan,
	}
}

func (m ProgressBarModel) Init() tea.Cmd {
	return tea.Batch(m.progress.Tick, m.tick)
}

func updateProgressCmd(progress float64) tea.Cmd {
	return func() tea.Msg {
		return progressMsg(progress)
	}
}

type tickMsg time.Time

type ProgressReader struct {
	reader     io.Reader
	totalSize  int64
	readBytes  int64
	progressFn func(readBytes int64)
}

// NewProgressReader creates a new ProgressReader.
func NewProgressReader(reader io.Reader, totalSize int64, progressFn func(readBytes int64)) *ProgressReader {
	return &ProgressReader{
		reader:     reader,
		totalSize:  totalSize,
		progressFn: progressFn,
	}
}

// Read implements the io.Reader interface.
func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.readBytes += int64(n)
	if pr.progressFn != nil {
		pr.progressFn(pr.readBytes)
		// Debug log to check progress
		log.Printf("ProgressReader: Read %d bytes, total %d bytes", n, pr.readBytes)
	}
	if err != nil {
		// Log error if not nil
		log.Printf("ProgressReader encountered an error: %v", err)
	}
	return n, err
}

// Close implements the io.Closer interface.
func (pr *ProgressReader) Close() error {
	if closer, ok := pr.reader.(io.Closer); ok {
		return closer.Close()
	}
	// If the underlying reader does not implement io.Closer, return nil
	return nil
}

func runProgressBarTUI(progressChan <-chan float64) {
	m := NewProgressBarModel()

	// Update model to handle progress updates
	m.Update = func(msg tea.Msg) (tea.Model, tea.Cmd) {
		switch msg := msg.(type) {
		case float64:
			m.progress.SetValue(msg) // Update the progress bar's value
			return m, tea.Batch(m.progress.Animate(), time.After(time.Millisecond*100))
		case tea.KeyMsg:
			if msg.String() == "q" {
				return m, tea.Quit
			}
		}
		return m, nil
	}

	// Start the TUI
	p := tea.NewProgram(m)
	if err := p.Start(); err != nil {
		log.Fatal(err)
	}
}
