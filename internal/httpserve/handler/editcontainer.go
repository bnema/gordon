package handler

import (
	"fmt"
	"strings"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/gotemplate/render"
	"github.com/bnema/gordon/pkg/utils/docker"
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

	fmt.Println(containerID)

	// Get the container info
	containerInfo, err := docker.GetContainerInfo(containerID)
	if err != nil {
		return sendError(c, err)
	}

	// Trim the slash from the container name
	containerInfo.Name = strings.TrimPrefix(containerInfo.Name, "/")
	// Get just the network names
	networkNames := make([]string, 0, len(containerInfo.NetworkSettings.Networks))
	for networkName := range containerInfo.NetworkSettings.Networks {
		networkNames = append(networkNames, networkName)
	}

	// Prepare Ports
	portMappings := make([]string, 0)
	for port, binding := range containerInfo.NetworkSettings.Ports {
		for _, b := range binding {
			portMappings = append(portMappings, fmt.Sprintf("%s:%s->%s", b.HostIP, b.HostPort, port))
		}
	}

	// Prepare Volumes (Mounts)
	volumeMappings := make([]string, 0)
	for _, mount := range containerInfo.Mounts {
		volumeMappings = append(volumeMappings, fmt.Sprintf("%s:%s", mount.Source, mount.Destination))
	}

	// Initialize a map to store the information you want to display
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

// ContainerManagerEditPOST is the route action for editing a container
func ContainerManagerEditPOST(c echo.Context, a *app.App) error {
	containerID := c.Param("ID")
	fmt.Println(containerID)

	// Get the new config
	newConfig := c.FormValue("container_config")

	fmt.Println(newConfig)

	return nil

}
