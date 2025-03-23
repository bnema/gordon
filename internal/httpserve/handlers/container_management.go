package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/load"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/humanize"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/docker/docker/api/types"
	"github.com/labstack/echo/v4"
)

type MapOperation string

var (
	idMap      = make(map[string]string)
	idMapMutex sync.Mutex
)

const (
	Fetch  MapOperation = "Fetch"
	Update MapOperation = "Update"
	Delete MapOperation = "Delete"
)

func safelyInteractWithIDMap(op MapOperation, key string, value ...string) (string, bool) {
	idMapMutex.Lock()
	defer idMapMutex.Unlock()

	switch op {
	case Fetch:
		val, exists := idMap[key]
		return val, exists
	case Update:
		if len(value) > 0 {
			idMap[key] = value[0]
		}
		return "", true
	case Delete:
		delete(idMap, key)
		return "", true
	default:
		return "", false
	}
}

type HumanReadableContainerImage struct {
	ID          string
	Name        string
	ShortID     string
	CreatedStr  string
	SizeStr     string
	RepoDigests []string
	RepoTags    []string
	Repository  string
	Tag         string
	Size        string
	Created     string
}

type HumanReadableContainer struct {
	*types.Container
	Name       string
	Ports      []string
	ShortID    string
	CreatedStr string
	SizeStr    string
	UpSince    string
	StateColor string
	ProxyPort  string
}

type ContainerDisplay struct {
	ID                string
	Name              string
	Ports             []docker.PortMapping
	ProxyPort         string
	CreatedStr        string
	Status            string
	ImageName         string
	IsGordonContainer bool
}

// renderHTML is a generalized function to render HTML
func renderHTML(c echo.Context, a *server.App, path, templateName string, data map[string]interface{}) error {
	logger.Debug("Rendering HTML", "path", path, "template", templateName, "data_keys", getKeysFromMap(data))

	rendererData, err := render.GetHTMLRenderer(path, templateName, a.TemplateFS, a)
	if err != nil {
		logger.Error("Error getting HTML renderer", "error", err)
		return err
	}

	renderedHTML, err := rendererData.Render(data, a)
	if err != nil {
		logger.Error("Error rendering HTML", "error", err)
		return err
	}

	logger.Debug("HTML rendered successfully", "content_length", len(renderedHTML))
	return c.HTML(http.StatusOK, renderedHTML)
}

// getKeysFromMap returns a slice of keys from a map
func getKeysFromMap(data map[string]interface{}) []string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	return keys
}

// ActionSuccess returns the success HTML fragment
func ActionSuccess(a *server.App) string {
	// Create a data structure to pass to the template
	data := map[string]interface{}{
		"Message": "Container deleted successfully!",
	}

	successFragment, err := load.Fragment(a, "success", data)
	if err != nil {
		fmt.Println(err)
		return ""
	}

	return successFragment
}

// ImageManagerComponent handles the /image-manager route (HTMX route)
func ImageManagerComponent(c echo.Context, a *server.App) error {
	logger.Debug("ImageManagerComponent called", "url", c.Request().URL.String())

	images, err := docker.ListContainerImages()
	if err != nil {
		logger.Error("Error listing container images:", "error", err)
		return sendError(c, err)
	}

	logger.Debug("Images found", "count", len(images))
	for i, img := range images {
		logger.Debug("Image details", "index", i, "id", img.ID, "repoTags", img.RepoTags)
	}

	var humanReadableImages []HumanReadableContainerImage

	for _, img := range images {
		shortID := img.ID[7:19]
		safelyInteractWithIDMap(Update, shortID, img.ID)
		createdTime := time.Unix(img.Created, 0)
		createdStr := humanize.TimeAgo(createdTime)
		sizeStr := humanize.BytesToReadableSize(img.Size)

		for _, repoTag := range img.RepoTags {
			// Split the repo tag into repository and tag
			parts := strings.Split(repoTag, ":")
			repository := parts[0]
			tag := "latest"
			if len(parts) > 1 {
				tag = parts[1]
			}

			humanReadableImages = append(humanReadableImages, HumanReadableContainerImage{
				ID:          img.ID,
				ShortID:     shortID,
				CreatedStr:  createdStr,
				SizeStr:     sizeStr,
				Name:        repoTag,
				RepoDigests: img.RepoDigests,
				RepoTags:    img.RepoTags,
				// Add fields that match template expectations
				Repository: repository,
				Tag:        tag,
				Size:       sizeStr,
				Created:    createdStr,
			})
		}
	}

	data := map[string]interface{}{
		"Images": humanReadableImages,
	}

	logger.Debug("Rendering imagelist template with", len(humanReadableImages), "images")
	return renderHTML(c, a, "html/fragments", "imagelist.gohtml", data)
}

// ImageManagerDelete handles the /image-manager/delete route
func ImageManagerDelete(c echo.Context, a *server.App) error {
	ShortID := c.Param("ID")

	imageID, exists := safelyInteractWithIDMap(Fetch, ShortID)
	if !exists {
		return c.String(http.StatusBadRequest, "Invalid ShortImgID")
	}

	err := docker.DeleteContainerImage(imageID)
	if err != nil {
		c.Response().Header().Set("X-Error-Type", "image")
		return c.String(http.StatusInternalServerError, err.Error())
	}

	safelyInteractWithIDMap(Delete, ShortID)

	return c.String(http.StatusOK, ActionSuccess(a))
}

// ContainerManagerComponent handles the /container-manager route
func ContainerManagerComponent(c echo.Context, a *server.App) error {
	logger.Debug("ContainerManagerComponent called", "url", c.Request().URL.String())

	containers, err := docker.ListRunningContainers()
	if err != nil {
		logger.Error("Error listing containers", "error", err)
		return sendError(c, err)
	}

	logger.Debug("Containers found", "count", len(containers))
	for i, container := range containers {
		logger.Debug("Container details", "index", i, "id", container.ID, "names", container.Names, "state", container.State)
	}

	var humanReadableContainers []HumanReadableContainer

	for _, container := range containers {
		localContainer := container // Make a local copy
		sizeStr := humanize.BytesToReadableSize(container.SizeRw)
		stateColor := "blue"

		if container.State == "running" {
			stateColor = "green"
		} else if container.State == "exited" {
			stateColor = "red"
		}
		var ports []string
		for _, port := range container.Ports {
			portStr := fmt.Sprintf("%d:%d", port.PublicPort, port.PrivatePort)
			ports = append(ports, portStr)
		}

		for _, name := range container.Names {
			name = name[1:]
			humanReadableContainers = append(humanReadableContainers, HumanReadableContainer{
				Container:  &localContainer,
				SizeStr:    sizeStr,
				UpSince:    humanize.TimeAgo(time.Unix(container.Created, 0)),
				StateColor: stateColor,
				Name:       name,
				Ports:      ports,
				ProxyPort:  extractProxyPort(container.Labels),
				CreatedStr: humanize.TimeAgo(time.Unix(container.Created, 0)),
			})
		}
	}
	yamlData, err := GetLocalizedData(c, a)
	if err != nil {
		return err
	}
	data := map[string]interface{}{
		"Lang":        yamlData["Lang"],
		"Containers":  humanReadableContainers,
		"NoContainer": len(humanReadableContainers) == 0,
	}

	logger.Debug("Rendering containerlist template",
		"humanReadableContainers", len(humanReadableContainers),
		"noContainer", len(humanReadableContainers) == 0)

	return renderHTML(c, a, "html/fragments", "containerlist.gohtml", data)
}

func extractProxyPort(labels map[string]string) string {
	// Check for the gordon proxy port label
	if port, ok := labels["gordon.proxy.port"]; ok {
		return port
	}

	// Backward compatibility for Traefik-labeled containers
	for key, value := range labels {
		if strings.Contains(key, "loadbalancer.server.port") {
			return value
		}
	}

	return ""
}

// ContainerManagerDelete handles the /container-manager/delete route
func ContainerManagerDelete(c echo.Context, a *server.App) error {
	err := docker.RemoveContainer(c.Param("ID"))
	if err != nil {
		return sendError(c, err)
	}
	return c.HTML(http.StatusOK, ActionSuccess(a))
}

// ContainerManagerStop handles the /container-manager/stop route
func ContainerManagerStop(c echo.Context, a *server.App) error {
	// Stop the container gracefully with a timeout
	stopped, err := docker.StopContainerGracefully(c.Param("ID"), 3*time.Second)
	if err != nil {
		return sendError(c, err)
	}
	// if stopped is true, the container was stopped gracefully
	if stopped {
		return c.HTML(http.StatusOK, ActionSuccess(a))
	}
	// If stopped is false, the container was not stopped gracefully we force stop it
	err = docker.StopContainerRagefully(c.Param("ID"))
	if err != nil {
		return sendError(c, err)
	}

	return c.HTML(http.StatusOK, ActionSuccess(a))
}

// ContainerManagerStart handles the /container-manager/start route
func ContainerManagerStart(c echo.Context, a *server.App) error {
	err := docker.StartContainer(c.Param("ID"))
	if err != nil {
		return sendError(c, err)
	}
	return c.HTML(http.StatusOK, ActionSuccess(a))
}
