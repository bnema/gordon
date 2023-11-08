package mvu

import (
	"fmt"
	"net/http"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

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

// RunProgressBarTUI runs the progress bar TUI and returns the final model
func RunProgressBarTUI(ProgressCh <-chan ProgressMsg) (*Model, error) {
	m := NewPBModel()
	p := tea.NewProgram(&m)
	// Start a goroutine that updates the progress bar as percentages are received on the channel.
	go func() {
		for percent := range ProgressCh {
			p.Send(ProgressMsg(percent))
		}
	}()

	if _, err := p.Run(); err != nil {
		fmt.Println("Error running progress bar TUI:", err)
		os.Exit(1)
	}

	return &m, nil
}
