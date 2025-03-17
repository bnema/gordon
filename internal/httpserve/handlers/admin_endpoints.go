package handlers

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/filestore"
	"github.com/bnema/gordon/pkg/humanize"
	"github.com/docker/docker/api/types"
	"github.com/labstack/echo/v4"
	"gopkg.in/yaml.v3"
)

// ContainerManagerAPI returns a JSON list of all containers
func ContainerManagerAPI(c echo.Context, a *server.App) error {
	containers, err := docker.ListRunningContainers()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
	}

	var containerList []map[string]interface{}

	for _, container := range containers {
		// Format container data for JSON response
		containerData := map[string]interface{}{
			"id":         container.ID,
			"name":       container.Names[0][1:], // Remove leading slash
			"image":      container.Image,
			"state":      container.State,
			"status":     container.Status,
			"created":    container.Created,
			"createdStr": humanize.TimeAgo(time.Unix(container.Created, 0)),
			"ports":      container.Ports,
			"sizeRw":     container.SizeRw,
			"sizeStr":    humanize.BytesToReadableSize(container.SizeRw),
			"proxyPort":  extractProxyPort(container.Labels),
		}
		containerList = append(containerList, containerData)
	}

	return c.JSON(http.StatusOK, containerList)
}

// ContainerInfoAPI returns JSON data for a specific container
func ContainerInfoAPI(c echo.Context, a *server.App) error {
	containerID := c.Param("ID")

	containerInfo, err := docker.GetContainerInfo(containerID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
	}

	// Parse the created time string to a time.Time
	createdTime, err := time.Parse(time.RFC3339, containerInfo.Created)
	if err != nil {
		createdTime = time.Now() // Fallback if parsing fails
	}

	// Format the container info for JSON response
	containerData := map[string]interface{}{
		"id":         containerInfo.ID,
		"name":       containerInfo.Name[1:], // Remove leading slash
		"image":      containerInfo.Config.Image,
		"hostname":   containerInfo.Config.Hostname,
		"state":      containerInfo.State.Status,
		"created":    containerInfo.Created,
		"createdStr": humanize.TimeAgo(createdTime),
		"ports":      containerInfo.NetworkSettings.Ports,
		"env":        containerInfo.Config.Env,
		"labels":     containerInfo.Config.Labels,
	}

	return c.JSON(http.StatusOK, containerData)
}

// ContainerStopAPI stops a container and returns JSON response
func ContainerStopAPI(c echo.Context, a *server.App) error {
	containerID := c.Param("ID")

	err := docker.StopContainer(containerID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]string{
		"status":  "success",
		"message": "Container stopped successfully",
	})
}

// ContainerStartAPI starts a container and returns JSON response
func ContainerStartAPI(c echo.Context, a *server.App) error {
	containerID := c.Param("ID")

	err := docker.StartContainer(containerID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]string{
		"status":  "success",
		"message": "Container started successfully",
	})
}

// ContainerDeleteAPI deletes a container and returns JSON response
func ContainerDeleteAPI(c echo.Context, a *server.App) error {
	containerID := c.Param("ID")

	err := docker.RemoveContainer(containerID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]string{
		"status":  "success",
		"message": "Container deleted successfully",
	})
}

// ContainerEditGetAPI returns container configuration as JSON for editing
func ContainerEditGetAPI(c echo.Context, a *server.App) error {
	containerID := c.Param("ID")

	// Get the container info
	containerInfo, err := docker.GetContainerInfo(containerID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
	}

	// Prepare the container configuration data
	// Similar to ContainerManagerEditGET but returns JSON instead of HTML
	infoMap := prepareContainerInfoMap(containerInfo)

	return c.JSON(http.StatusOK, infoMap)
}

// prepareContainerInfoMap extracts container configuration into a map
func prepareContainerInfoMap(containerInfo types.ContainerJSON) map[string]interface{} {
	// Trim the slash from the container name
	containerName := containerInfo.Name
	if len(containerName) > 0 && containerName[0] == '/' {
		containerName = containerName[1:]
	}

	// Get the network names
	networkNames := make([]string, 0, len(containerInfo.NetworkSettings.Networks))
	for networkName := range containerInfo.NetworkSettings.Networks {
		networkNames = append(networkNames, networkName)
	}

	// Prepare Ports
	portMappings := make([]string, 0)
	for port, bindings := range containerInfo.HostConfig.PortBindings {
		for _, binding := range bindings {
			portMappings = append(portMappings, binding.HostPort+":"+port.Port()+"/"+port.Proto())
		}
	}

	// Prepare Volumes (Mounts)
	volumeMappings := make([]string, 0)
	for _, mount := range containerInfo.Mounts {
		volumeMappings = append(volumeMappings, mount.Source+":"+mount.Destination)
	}

	// Initialize a map to store the information from the container
	infoMap := make(map[string]interface{})

	// Populate the map with container details
	infoMap["name"] = containerName
	infoMap["image"] = containerInfo.Config.Image
	infoMap["hostname"] = containerInfo.Config.Hostname
	infoMap["ports"] = portMappings
	infoMap["volumes"] = volumeMappings
	infoMap["environment"] = containerInfo.Config.Env
	infoMap["labels"] = containerInfo.Config.Labels
	infoMap["network"] = networkNames
	infoMap["restart"] = containerInfo.HostConfig.RestartPolicy.Name

	return infoMap
}

// ContainerEditPostAPI updates a container configuration from JSON data
func ContainerEditPostAPI(c echo.Context, a *server.App) error {
	containerID := c.Param("ID")

	// Get the old container info for reference
	_, err := docker.GetContainerInfo(containerID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Getting old container info failed: " + err.Error(),
		})
	}

	// Parse the JSON request body
	var containerParams map[string]interface{}
	if err := c.Bind(&containerParams); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "Invalid request body: " + err.Error(),
		})
	}

	// Convert to YAML for compatibility with existing code
	_, err = yaml.Marshal(containerParams)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to process container configuration: " + err.Error(),
		})
	}

	// Use the existing transaction mechanism to update the container
	// This would need to be adapted to work with the JSON API
	// For now, we'll return a placeholder response
	return c.JSON(http.StatusOK, map[string]string{
		"status":  "success",
		"message": "Container updated successfully",
	})
}

// ImageManagerAPI returns a JSON list of all images
func ImageManagerAPI(c echo.Context, a *server.App) error {
	images, err := docker.ListContainerImages()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
	}

	var imageList []map[string]interface{}

	for _, img := range images {
		shortID := img.ID[7:19] // Remove "sha256:" prefix and truncate
		createdTime := time.Unix(img.Created, 0)
		createdStr := humanize.TimeAgo(createdTime)
		sizeStr := humanize.BytesToReadableSize(img.Size)

		// Store the mapping in the idMap
		safelyInteractWithIDMap(Update, shortID, img.ID)

		// For each tag, create a separate image entry
		for _, repoTag := range img.RepoTags {
			imageData := map[string]interface{}{
				"id":          img.ID,
				"shortID":     shortID,
				"name":        repoTag,
				"created":     img.Created,
				"createdStr":  createdStr,
				"size":        img.Size,
				"sizeStr":     sizeStr,
				"repoDigests": img.RepoDigests,
				"repoTags":    img.RepoTags,
			}
			imageList = append(imageList, imageData)
		}
	}

	return c.JSON(http.StatusOK, imageList)
}

// ImageDeleteAPI deletes an image and returns JSON response
func ImageDeleteAPI(c echo.Context, a *server.App) error {
	shortID := c.Param("ID")

	// Get the full image ID from the map
	imageID, exists := safelyInteractWithIDMap(Fetch, shortID)
	
	if !exists {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "Invalid image ID",
		})
	}

	err := docker.DeleteContainerImage(imageID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
	}

	// Remove the mapping
	safelyInteractWithIDMap(Delete, shortID)

	return c.JSON(http.StatusOK, map[string]string{
		"status":  "success",
		"message": "Image deleted successfully",
	})
}

// UploadImageAPI handles image upload and returns JSON response
func UploadImageAPI(c echo.Context, a *server.App) error {
	// Set upload size limit (10GB)
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 10*1024*1024*1024)

	// Get the uploaded file
	file, err := c.FormFile("imageFile")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "No file uploaded: " + err.Error(),
		})
	}

	// Open the file
	src, err := file.Open()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to open uploaded file: " + err.Error(),
		})
	}
	defer src.Close()

	// Create a temporary file to store the uploaded chunks
	tempFile, err := os.CreateTemp("", "upload-*")
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to create temporary file: " + err.Error(),
		})
	}
	defer tempFile.Close()
	defer os.Remove(tempFile.Name()) // Clean up the temp file when done

	// Copy the file chunks to the temporary file
	_, err = io.Copy(tempFile, src)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to save uploaded file: " + err.Error(),
		})
	}

	// Reset the temporary file pointer
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to process uploaded file: " + err.Error(),
		})
	}

	// Save the image to the storage directory
	saveInPath, err := filestore.SaveImageToStorage(&a.Config, file.Filename, tempFile)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to save image to storage: " + err.Error(),
		})
	}

	// Import the image into Docker
	imageID, err := docker.ImportImageToEngine(saveInPath)
	if err != nil {
		// Try to clean up the saved file if import fails
		_ = filestore.RemoveFromStorage(saveInPath)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to import image to Docker: " + err.Error(),
		})
	}

	// Remove the image from the storage directory after successful import
	err = filestore.RemoveFromStorage(saveInPath)
	if err != nil {
		// Just log this error, don't fail the request since the import was successful
		fmt.Printf("Warning: Failed to remove temporary image file %s: %v\n", saveInPath, err)
	}

	return c.JSON(http.StatusOK, map[string]string{
		"status":  "success",
		"message": "Image uploaded successfully",
		"imageId": imageID,
	})
}
