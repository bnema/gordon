// gordon/internal/cli/handler/deploy_survey.go

package handler

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/common"
	"github.com/charmbracelet/log"
)

func HandleExistingContainer(app *cli.App, conflictResp *common.ConflictCheckResponse) error {
	// Add a small delay to ensure terminal is clear
	time.Sleep(100 * time.Millisecond)

	// Clear the current line
	fmt.Print("\033[2K\r")

	containerName := conflictResp.ContainerName
	shortID := conflictResp.ContainerID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}

	var proceed bool
	prompt := &survey.Confirm{
		Message: fmt.Sprintf("Container '%s' (ID: %s) is already running for %s. Stop and remove it?",
			containerName,
			shortID,
			conflictResp.RunningTime,
		),
		Default: true,
	}

	if err := survey.AskOne(prompt, &proceed); err != nil {
		return fmt.Errorf("survey failed: %w", err)
	}

	if !proceed {
		return fmt.Errorf("operation cancelled by user")
	}

	log.Info("Stopping container...", "name", containerName, "id", shortID)

	// Stop the container
	if err := stopContainer(app, conflictResp.ContainerID); err != nil {
		return fmt.Errorf("failed to stop container '%s': %w", containerName, err)
	}

	log.Info("Removing container...", "name", containerName, "id", shortID)

	// Remove the container
	if err := removeContainer(app, conflictResp.ContainerID); err != nil {
		return fmt.Errorf("failed to remove container '%s': %w", containerName, err)
	}

	log.Info("Container stopped and removed successfully",
		"name", containerName,
		"id", shortID,
	)
	return nil
}

func removeContainer(a *cli.App, containerID string) error {
	reqPayload := common.RequestPayload{
		Type: "remove",
		Payload: common.RemovePayload{
			ContainerID: containerID,
		},
	}

	resp, err := SendHTTPRequest(a, &reqPayload, "POST", "/remove")
	if err != nil {
		return err
	}

	var removeResponse common.RemoveResponse
	if err := json.Unmarshal(resp.Body, &removeResponse); err != nil {
		return fmt.Errorf("failed to parse remove response: %w", err)
	}

	if !removeResponse.Success {
		return fmt.Errorf("failed to remove container: %s", removeResponse.Message)
	}

	return nil
}

func stopContainer(a *cli.App, containerID string) error {
	reqPayload := common.RequestPayload{
		Type: "stop",
		Payload: common.StopPayload{
			ContainerID: containerID,
		},
	}

	resp, err := SendHTTPRequest(a, &reqPayload, "POST", "/stop")
	if err != nil {
		return err
	}

	var stopResponse common.StopResponse
	if err := json.Unmarshal(resp.Body, &stopResponse); err != nil {
		return fmt.Errorf("failed to parse stop response: %w", err)
	}

	if !stopResponse.Success {
		return fmt.Errorf("failed to stop container: %s", stopResponse.Message)
	}

	return nil
}
