package handler

import (
	"fmt"
	"log"
	"strings"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/labstack/echo/v4"
	"gopkg.in/yaml.v3"
)

func generateOrderedYAML(infoMap map[string]interface{}) (string, error) {
	orderedKeys := []string{"Name", "Image", "Hostname", "Ports", "Environment", "Labels", "Network", "Volumes", "Restart"}

	var yamlString string
	for _, key := range orderedKeys {
		value, exists := infoMap[key]
		if !exists {
			continue
		}

		yamlBytes, err := yaml.Marshal(map[string]interface{}{key: value})
		if err != nil {
			return "", err
		}

		yamlString += string(yamlBytes)
	}

	return yamlString, nil
}

// ContainerManagerEditGET displays the edit container view
func ContainerManagerEditGET(c echo.Context, a *app.App) error {
	containerID := c.Param("ID")

	// Get the container info
	containerInfo, err := docker.GetContainerInfo(containerID)
	if err != nil {
		return sendError(c, err)
	}

	// Trim the slash from the container name
	containerInfo.Name = strings.TrimPrefix(containerInfo.Name, "/")
	// Get the network names
	networkNames := make([]string, 0, len(containerInfo.NetworkSettings.Networks))
	for networkName := range containerInfo.NetworkSettings.Networks {
		networkNames = append(networkNames, networkName)
	}

	// Prepare Ports
	portMappings := make([]string, 0)
	for port, bindings := range containerInfo.HostConfig.PortBindings {
		for _, binding := range bindings {
			portMappings = append(portMappings, fmt.Sprintf("%s:%s/%s", binding.HostPort, port.Port(), port.Proto()))
		}
	}

	// Prepare Volumes (Mounts)
	volumeMappings := make([]string, 0)
	for _, mount := range containerInfo.Mounts {
		volumeMappings = append(volumeMappings, fmt.Sprintf("%s:%s", mount.Source, mount.Destination))
	}

	// Initialize a map to store the information from the container
	infoMap := make(map[string]interface{})

	// Populate the map with container details
	infoMap["Name"] = containerInfo.Name
	infoMap["Image"] = containerInfo.Config.Image
	infoMap["Hostname"] = containerInfo.Config.Hostname
	infoMap["Ports"] = portMappings
	infoMap["Volumes"] = volumeMappings
	infoMap["Environment"] = containerInfo.Config.Env
	infoMap["Labels"] = containerInfo.Config.Labels
	infoMap["Network"] = networkNames
	infoMap["Restart"] = containerInfo.HostConfig.RestartPolicy.Name

	// Convert the infoMap to YAML format
	yamlString, err := generateOrderedYAML(infoMap)
	if err != nil {
		return sendError(c, err)
	}

	// Pass the containerInfo to the data map
	data := map[string]interface{}{
		"Title":         "Edit container",
		"ID":            containerID,
		"ContainerInfo": yamlString,
	}

	rendererData, err := render.GetHTMLRenderer("html/fragments", "editcontainer.gohtml", a.TemplateFS, a)

	if err != nil {
		return sendError(c, err)
	}

	renderedHTML, err := rendererData.Render(data, a)

	if err != nil {
		return sendError(c, err)
	}

	return c.HTML(200, renderedHTML)
}

type TransactionStep func() error
type RollbackStep func() error

type TransactionQueue struct {
	NewContainerID    string
	NewContainerName  string
	TempContainerName string
	OldContainerID    string
	OldContainerName  string
	steps             []TransactionStep
	rollbacks         []RollbackStep
}

func (tq *TransactionQueue) Add(step TransactionStep, rollback RollbackStep) {
	tq.steps = append(tq.steps, step)
	tq.rollbacks = append(tq.rollbacks, rollback)
}

func (tq *TransactionQueue) Execute() error {
	var transactionErrors []error
	var rollbackErrors []error
	for i, step := range tq.steps {
		err := step()
		if err != nil {
			log.Printf("Error occurred at step index %d: %v", i, err)
			transactionErrors = append(transactionErrors, err)
			for j := i - 1; j >= 0; j-- {
				rollback := tq.rollbacks[j]
				if rollback != nil {
					if rollbackErr := rollback(); rollbackErr != nil {
						log.Printf("Failed to execute rollback at index %d: %v", j, rollbackErr)
						rollbackErrors = append(rollbackErrors, rollbackErr)
					}
				}
			}
			return fmt.Errorf("transaction failed: %v; rollback errors: %v", transactionErrors, rollbackErrors)
		}
	}
	return nil
}

// ContainerManagerEditPOST handles the edit container form submission
func ContainerManagerEditPOST(c echo.Context, a *app.App) error {
	tq := &TransactionQueue{}
	oldContainerID := c.Param("ID")

	oldContainerInfo, err := docker.GetContainerInfo(oldContainerID)
	if err != nil {
		return sendError(c, fmt.Errorf("getting old container info failed: %w", err))
	}

	containerConfig := c.FormValue("container_config")
	var containerParams render.YAMLContainerParams
	err = yaml.Unmarshal([]byte(containerConfig), &containerParams)
	if err != nil {
		return sendError(c, fmt.Errorf("YAML unmarshal failed: %w", err))
	}

	// Use render.FromYAMLStructToCmdParams to convert YAMLContainerParams to ContainerCommandParams
	cmdParams, err := render.FromYAMLStructToCmdParams(containerParams)
	if err != nil {
		return fmt.Errorf("failed to convert YAML: %w", err)
	}

	tq.OldContainerID = oldContainerID
	tq.OldContainerName = strings.TrimPrefix(oldContainerInfo.Name, "/")
	tq.NewContainerName = cmdParams.ContainerName
	tq.TempContainerName = fmt.Sprintf("%s-temp", tq.NewContainerName)

	cmdParams.ContainerName = tq.TempContainerName

	// 1. Stop the old container
	tq.Add(StopOldContainerStep(tq), StartOldContainerRollback(tq))

	// 2. Create the new container
	tq.Add(CreateNewContainerStep(tq, cmdParams), RemoveNewContainerRollback(tq))

	// 3. Start the new container
	tq.Add(StartNewContainerStep(tq), StopNewContainerRollback(tq))

	// 4. Rename the new container to the original name
	tq.Add(RemoveOldContainerStep(tq), nil)

	// 5. Remove the old container
	tq.Add(RenameNewContainerStep(tq), RenameNewContainerRollback(tq))

	err = tq.Execute()
	if err != nil {
		return sendError(c, fmt.Errorf("transaction failed: %w", err))
	}

	return c.HTML(200, ActionSuccess(a))
}

// CreateNewContainerStep creates a new container with the given parameters
func CreateNewContainerStep(tq *TransactionQueue, cmdParams docker.ContainerCommandParams) TransactionStep {
	return func() error {
		var err error
		tq.NewContainerID, err = docker.CreateContainer(cmdParams)
		if err != nil {
			return fmt.Errorf("creating new container failed: %w", err)
		}
		return nil
	}
}

// StartNewContainerStep starts the new container
func StartNewContainerStep(tq *TransactionQueue) TransactionStep {
	return func() error {
		err := docker.StartContainer(tq.NewContainerID)
		if err != nil {
			return fmt.Errorf("starting new container failed: %w", err)
		}
		return nil
	}
}

// StopOldContainerStep stops the old container
func StopOldContainerStep(tq *TransactionQueue) TransactionStep {
	return func() error {
		err := docker.StopContainer(tq.OldContainerID)
		if err != nil {
			return fmt.Errorf("stopping old container failed: %w", err)
		}
		return nil
	}
}

// RemoveOldContainerStep removes the old container
func RemoveOldContainerStep(tq *TransactionQueue) TransactionStep {
	return func() error {
		err := docker.RemoveContainer(tq.OldContainerID)
		if err != nil {
			return fmt.Errorf("removing old container failed: %w", err)
		}
		return nil
	}
}

// RemoveNewContainerRollback removes the new container if the transaction fails
func RemoveNewContainerRollback(tq *TransactionQueue) RollbackStep {
	return func() error {
		err := docker.RemoveContainer(tq.NewContainerID)
		if err != nil {
			return fmt.Errorf("rollback: removing new container failed: %w", err)
		}
		return nil
	}
}

// StopNewContainerRollback stops the new container if the transaction fails
func StopNewContainerRollback(tq *TransactionQueue) RollbackStep {
	return func() error {
		err := docker.StopContainer(tq.NewContainerID)
		if err != nil {
			return fmt.Errorf("rollback: stopping new container failed: %w", err)
		}
		return nil
	}
}

// StartOldContainerRollback starts the old container if the transaction fails
func StartOldContainerRollback(tq *TransactionQueue) RollbackStep {
	return func() error {
		err := docker.StartContainer(tq.OldContainerID)
		if err != nil {
			return fmt.Errorf("rollback: starting old container failed: %w", err)
		}
		return nil
	}
}

// RenameNewContainerStep renames the new temporary container to the original name
func RenameNewContainerStep(tq *TransactionQueue) TransactionStep {
	return func() error {
		err := docker.RenameContainer(tq.NewContainerID, tq.NewContainerName)
		if err != nil {
			return fmt.Errorf("renaming new container failed: %w", err)
		}
		return nil
	}
}

// RenameNewContainerRollback renames the new container back to the temporary name if it fails
func RenameNewContainerRollback(tq *TransactionQueue) RollbackStep {
	return func() error {
		err := docker.RenameContainer(tq.NewContainerID, tq.TempContainerName)
		if err != nil {
			return fmt.Errorf("rollback: renaming new container back failed: %w", err)
		}
		return nil
	}
}
