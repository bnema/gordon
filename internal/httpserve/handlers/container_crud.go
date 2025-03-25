package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/cmdparams"
	templcomponents "github.com/bnema/gordon/internal/templating/models/templ/components"
	templadmin "github.com/bnema/gordon/internal/templating/models/templ/pages/admin"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/sanitize"
	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
)

// FromShortIDToImageID converts a short image ID to a full image ID
func FromShortIDToImageID(ShortID string) (string, error) {
	imageID, exists := safelyInteractWithIDMap(Fetch, ShortID)
	if !exists {
		log.Debug("No mapping found for short ID: %s", ShortID)
		return "", fmt.Errorf("image ID not found")
	}
	return imageID, nil
}

// CreateContainerRoute is the route for creating a new container
func CreateContainerGET(c echo.Context, a *server.App) error {
	log.Debug("Full request URL: %s", c.Request().URL.String())
	// Retrieve the ShortID of the image from the URL
	shortID := c.Param("ID")
	log.Debug("Received ShortID: %s", shortID)

	fullID, exists := safelyInteractWithIDMap(Fetch, shortID)
	if !exists {
		log.Debug("No mapping found for short ID: %s", shortID)
		return RenderNotFoundPage(c, a)
	}

	log.Debug("Found mapping: %s -> %s", shortID, fullID)

	// Get the image info
	imageInfo, err := docker.GetImageInfo(fullID)
	if err != nil {
		return sendError(c, err)
	}

	// Extract image name
	var imageName string
	if len(imageInfo.RepoTags) > 0 {
		imageName = imageInfo.RepoTags[0]
	} else {
		imageName = imageInfo.ID
	}

	// Setup form data
	formData := templcomponents.ContainerFormData{
		ImageName:    imageName,
		ImageID:      fullID,
		ShortID:      shortID,
		ErrorMessage: "",
	}

	// Get the renderer
	renderer := render.NewTemplRenderer(a)

	// Render the component
	return renderer.RenderTempl(c, templcomponents.CreateContainerForm(formData))
}

// CreateContainerFullGET handles the full HTML page for creating a new container
func CreateContainerFullGET(c echo.Context, a *server.App) error {
	// Retrieve the ShortID of the image from the URL
	shortID := c.Param("ID")
	log.Debug("Received ShortID: %s", shortID)

	fullID, exists := safelyInteractWithIDMap(Fetch, shortID)
	if !exists {
		log.Debug("No mapping found for short ID: %s", shortID)
		return RenderNotFoundPage(c, a)
	}

	log.Debug("Found mapping: %s -> %s", shortID, fullID)

	// Get the image info
	imageInfo, err := docker.GetImageInfo(fullID)
	if err != nil {
		return sendError(c, err)
	}

	// Extract image name
	var imageName string
	if len(imageInfo.RepoTags) > 0 {
		imageName = imageInfo.RepoTags[0]
	} else {
		imageName = imageInfo.ID
	}

	// Setup form data
	formData := templcomponents.ContainerFormData{
		ImageName:    imageName,
		ImageID:      fullID,
		ShortID:      shortID,
		ErrorMessage: "",
	}

	// Get the renderer
	renderer := render.NewTemplRenderer(a)

	// Render the createcontainerfull template
	pageData := templadmin.CreateContainerFullData{
		Title:         "Create Container | Gordon",
		BuildVersion:  a.Config.Build.BuildVersion,
		AdminPath:     a.Config.Admin.Path,
		UserSettings:  "/user",
		LogoutURL:     a.Config.Admin.Path + "/logout",
		ContainerData: formData,
	}

	return renderer.RenderTempl(c, templadmin.CreateContainerFullPage(pageData))
}

// CreateContainerPOST handles the create container form submission
func CreateContainerPOST(c echo.Context, a *server.App) error {
	log.Debug("CreateContainerPOST handler called")

	// Retrieve the ShortID of the image from the URL
	ShortID := c.Param("ID")
	log.Debug("Received ShortID for container creation:", "ShortID", ShortID)

	// Convert the ShortID to a full image ID
	imageID, err := FromShortIDToImageID(ShortID)
	if err != nil {
		log.Error("Error converting ShortID to full image ID:", "error", err)
		return sendError(c, err)
	}
	log.Debug("Converted ShortID to full image ID:", "imageID", imageID)

	// Get the image info
	_, err = docker.GetImageInfo(imageID)
	if err != nil {
		log.Error("Error getting image info:", "error", err)
		return sendError(c, err)
	}

	// Retrieve all form parameters
	formParams, err := c.FormParams()
	if err != nil {
		return sendError(c, err)
	}

	// Initialize a map to store sanitized values
	sanitizedInputs := make(map[string]string)

	// Iterate over all form parameters and sanitize them
	for k, v := range formParams {
		if len(v) > 0 {
			sanitized, err := sanitize.SanitizeHTML(v[0])
			if err != nil {
				return sendError(c, err)
			}
			sanitizedInputs[k] = sanitized
		}
	}

	cmdParams, err := cmdparams.FromInputsToCmdParams(sanitizedInputs, a)
	if err != nil {
		return sendError(c, err)
	}

	// Create the container
	containerID, err := docker.CreateContainer(cmdParams)
	if err != nil {
		return sendError(c, err)
	}

	// Start the container
	err = docker.StartContainer(containerID)
	if err != nil {
		// If start fails, try to cleanup the container
		log.Warn("Failed to start container", "error", err)
		_ = docker.RemoveContainer(containerID)
		return sendError(c, fmt.Errorf("failed to start container: %w", err))
	}

	// Get the container name for logging and as fallback
	containerName, err := docker.GetContainerName(containerID)
	if err != nil {
		log.Warn("Failed to get container name", "error", err)
	} else {
		containerName = strings.TrimPrefix(containerName, "/")
	}

	// Check if we should skip proxy setup
	skipProxySetup := false
	if skipProxyValue, exists := sanitizedInputs["skip_proxy_setup"]; exists {
		skipProxySetup = skipProxyValue == "true" || skipProxyValue == "1" || skipProxyValue == "yes"
	}

	// Only set up proxy if not skipped
	if !skipProxySetup {
		// Get container IP
		containerIP := GetContainerIP(a, containerID, containerName)

		// Construct target domain from form inputs
		var targetDomain string
		if sanitizedInputs["container_subdomain"] == "" {
			// If subdomain is empty, use just the domain
			targetDomain = fmt.Sprintf("%s://%s",
				sanitizedInputs["container_protocol"],
				sanitizedInputs["container_domain"])
		} else {
			// If subdomain is provided, use subdomain.domain format
			targetDomain = fmt.Sprintf("%s://%s.%s",
				sanitizedInputs["container_protocol"],
				sanitizedInputs["container_subdomain"],
				sanitizedInputs["container_domain"])
		}

		// Add proxy route for the new container
		err = AddProxyRoute(a, containerID, containerIP, sanitizedInputs["container_port"], targetDomain)
		if err != nil {
			log.Error("Failed to add proxy route", "error", err)
			// Continue anyway, as the container is already created and started
		}
	} else {
		log.Info("Skipping proxy setup as requested", "container_id", containerID, "container_name", containerName)
	}

	// Redirect to the container list page
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("%s/containers", a.Config.Admin.Path))
}
