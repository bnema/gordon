package mvu

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	defaultMaxRetries = 20
	httpTimeout       = 10 * time.Second
	retryInterval     = 1 * time.Second
)

type SpinnerModel struct {
	spinner        spinner.Model
	quitting       bool
	err            error
	deployment     DeploymentInfo
	DeploymentDone bool
}

type DeploymentInfo struct {
	imageName    string
	targetDomain string
	port         string
	client       *http.Client
	retryCount   int
	maxRetries   int
}

type urlCheckedMsg struct {
	success bool
	err     error
}

// NewSpinnerModel returns a new SpinnerModel
func NewSpinnerModel() SpinnerModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#54baff"))

	return SpinnerModel{
		spinner: s,
		deployment: DeploymentInfo{
			maxRetries: defaultMaxRetries,
			client:     newHTTPClient(),
		},
	}
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: httpTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

func (m SpinnerModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.checkURL())
}

// checkURL returns a command that checks the URL and returns a urlCheckedMsg
func (m *SpinnerModel) checkURL() tea.Cmd {
	return func() tea.Msg {
		success, err := attemptURLCheck(m.deployment)
		if err != nil {
			return urlCheckedMsg{success: false, err: err}
		}
		return urlCheckedMsg{success: success, err: nil}
	}
}

// attemptURLCheck attempts to check the URL and returns a bool and error
func attemptURLCheck(deployment DeploymentInfo) (bool, error) {
	for deployment.retryCount = 0; deployment.retryCount < deployment.maxRetries; deployment.retryCount++ {
		resp, err := deployment.client.Get(deployment.targetDomain)
		if err != nil {
			return false, fmt.Errorf("error checking URL: %w", err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return true, nil
		}
		time.Sleep(retryInterval)
	}
	return false, fmt.Errorf("the URL %s could not be reached after %d attempts", deployment.targetDomain, deployment.retryCount)
}

// Update handles the messages sent to the model
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

// View returns the view for the model
func (m SpinnerModel) View() string {
	if m.quitting {
		if m.err != nil {
			return fmt.Sprintf("Error: %v\n", m.err)
		}
		return "Deployment cancelled.\n"
	}

	// Use the textStyle to format the entire string
	return fmt.Sprintf("\n\n	%s Wait while Traefik is setting up the domain and certificates... \n\n", m.spinner.View())
}

// RunDeploymentTUI runs the complete deployment TUI and returns the final model
func RunDeploymentTUI(client *http.Client, imageName, targetDomain, port string) (SpinnerModel, error) {
	m := NewSpinnerModel()
	m.deployment.imageName = imageName
	m.deployment.targetDomain = targetDomain
	m.deployment.port = port
	m.deployment.client = client

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
