package model

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type SpinnerModel struct {
	spinner        spinner.Model
	quitting       bool
	err            error
	imageName      string
	targetDomain   string
	port           string
	client         *http.Client
	retryCount     int
	maxRetries     int
	DeploymentDone bool
}

type urlCheckedMsg struct {
	success bool
	err     error
}

func NewSpinnerModel() SpinnerModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	spinnerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	s.Style = spinnerStyle

	return SpinnerModel{
		spinner:    s,
		maxRetries: 20,
	}
}

func (m SpinnerModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, checkURL(m))
}

func (m SpinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		default:
			return m, nil
		}
	case urlCheckedMsg:
		if msg.success {
			m.DeploymentDone = true
			return m, tea.Quit
		} else {
			m.err = msg.err
			return m, nil
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	default:
		return m, nil
	}
}

func (m SpinnerModel) View() string {
	if m.quitting {
		if m.err != nil {
			return fmt.Sprintf("Error: %v\n", m.err)
		}
		return "Deployment cancelled.\n"
	}

	// Use the textStyle to format the entire string
	return fmt.Sprintf("\n\n %s Wait while Traefik is setting up the domain and certificates...\n\n", m.spinner.View())
}

func checkURL(m SpinnerModel) tea.Cmd {
	return func() tea.Msg {
		ch := make(chan urlCheckedMsg)

		go func() {
			defer close(ch) // Ensure the channel is closed when the goroutine exits
			// Initialize the HTTP client if not already done
			if m.client == nil {
				m.client = &http.Client{
					Timeout: time.Second * 10, // Set a timeout for the request
					Transport: &http.Transport{
						TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // Skip TLS verification, not recommended for production
					},
				}
			}

			// Try to check the URL with retries
			for i := 0; i < m.maxRetries; i++ {
				resp, err := m.client.Get(m.targetDomain) // Send the request
				if err != nil {
					return
				} else {
					// Print the response status code
					resp.Body.Close() // Close the body to avoid resource leaks
					if resp.StatusCode == http.StatusOK {
						ch <- urlCheckedMsg{success: true, err: nil}
						return
					}
				}

				m.retryCount++
				time.Sleep(time.Second * 1) // Wait for some time before retrying
			}

			// If the code reaches here (maxRetries exceeded) then send a message to the channel
			ch <- urlCheckedMsg{
				success: false,
				err:     fmt.Errorf("the URL %s could not be reached after %d attempts"+m.targetDomain, m.retryCount),
			}
		}()

		return waitForURLCheckedMsg(ch)
	}
}

func waitForURLCheckedMsg(ch chan urlCheckedMsg) tea.Msg {
	msg := <-ch // Wait for a message from the channel
	return msg  // Return the message to the model's update function
}

// Extracted function to run the TUI program and handle the final model
func RunDeploymentTUI(client *http.Client, imageName, targetDomain, port string) (SpinnerModel, error) {
	m := NewSpinnerModel()
	m.imageName = imageName
	m.targetDomain = targetDomain
	m.port = port
	m.client = client

	p := tea.NewProgram(m)
	modelInterface, err := p.Run()
	if err != nil {
		return SpinnerModel{}, fmt.Errorf("error running TUI program: %w", err)
	}

	finalModel, ok := modelInterface.(SpinnerModel)
	if !ok {
		return SpinnerModel{}, fmt.Errorf("could not type assert tea model to concrete type")
	}

	return finalModel, nil
}
