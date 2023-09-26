package handler

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/gotemplate/render"
	"github.com/bnema/gordon/pkg/utils/docker"
	"github.com/bnema/gordon/pkg/utils/humanize"
	"github.com/docker/docker/api/types"
	"github.com/labstack/echo/v4"
)

var idMap = make(map[string]string)
var idMapMutex sync.Mutex

type HumanReadableContainerImage struct {
	*types.ImageSummary
	Name       string
	ShortID    string
	CreatedStr string
	SizeStr    string
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
}

// ImageManagerComponent handles the /image-manager route (HTMX route)
func ImageManagerComponent(c echo.Context, a *app.App) error {
	images, err := docker.ListContainerImages()
	if err != nil {
		return sendError(c, err)
	}
	var humanReadableImages []HumanReadableContainerImage

	for _, image := range images {
		ShortID := image.ID[7:19]
		idMapMutex.Lock()
		idMap[ShortID] = image.ID
		idMapMutex.Unlock()
		createdTime := time.Unix(image.Created, 0)
		createdStr := humanize.TimeAgo(createdTime)
		sizeStr := humanize.BytesToReadableSize(image.Size)
		for _, repoTag := range image.RepoTags {
			humanReadableImages = append(humanReadableImages, HumanReadableContainerImage{
				ImageSummary: &types.ImageSummary{
					ID: ShortID, // Set the ID to the ShortID
				},
				CreatedStr: createdStr,
				SizeStr:    sizeStr,
				Name:       repoTag,
			})
		}
	}
	data := map[string]interface{}{
		"Images": humanReadableImages,
	}

	rendererData, err := render.GetHTMLRenderer("html/fragments", "imagelist.gohtml", a.TemplateFS, a)
	if err != nil {
		return err
	}
	renderedHTML, err := rendererData.Render(data, a)
	if err != nil {
		return err
	}
	return c.HTML(200, renderedHTML)
}

// ImageManagerDelete handles the /image-manager/delete route
func ImageManagerDelete(c echo.Context, a *app.App) error {
	// Get the ShortImgID from the URL
	ShortID := c.Param("ID")

	idMapMutex.Lock()
	// Check if the ShortImgID exists in the idMap
	imageID, exists := idMap[ShortID]
	idMapMutex.Unlock()

	if !exists {
		return c.String(http.StatusBadRequest, "Invalid ShortImgID")
	}

	err := docker.DeleteContainerImage(imageID)
	if err != nil {
		c.Response().Header().Set("X-Error-Type", "image")
		return c.String(http.StatusInternalServerError, err.Error())
	}

	// Remove the entry from idMap
	idMapMutex.Lock()
	delete(idMap, ShortID)
	idMapMutex.Unlock()

	// Since it is HTMX we return a HTML div with the message "Removed"
	return c.HTML(http.StatusOK, `<div>Removed</div>`)
}

// ContainerManagerComponent handles the /container-manager route
func ContainerManagerComponent(c echo.Context, a *app.App) error {
	containers, err := docker.ListRunningContainers()
	if err != nil {
		return sendError(c, err)
	}

	var humanReadableContainers []HumanReadableContainer

	for _, container := range containers {
		localContainer := container // Make a local copy
		createdTime := time.Unix(container.Created, 0)
		createdStr := humanize.TimeAgo(createdTime)
		sizeStr := humanize.BytesToReadableSize(container.SizeRw)
		stateColor := "green"

		if container.State == "exited" {
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
				CreatedStr: createdStr,
				SizeStr:    sizeStr,
				UpSince:    humanize.TimeAgo(time.Unix(container.Created, 0)),
				StateColor: stateColor,
				Name:       name,
				Ports:      ports,
			})
		}
	}
	data := map[string]interface{}{
		"containers": humanReadableContainers,
	}

	rendererData, err := render.GetHTMLRenderer("html/fragments", "containerlist.gohtml", a.TemplateFS, a)
	if err != nil {
		return err
	}
	renderedHTML, err := rendererData.Render(data, a)
	if err != nil {
		return err
	}
	return c.HTML(200, renderedHTML)
}

// ContainerManagerDelete handles the /container-manager/delete route
func ContainerManagerDelete(c echo.Context, a *app.App) error {
	ID := c.Param("ID")
	err := docker.DeleteContainer(ID)
	if err != nil {
		return sendError(c, err)
	}
	return c.HTML(http.StatusOK, `Success`)
}

// ContainerManagerStop handles the /container-manager/stop route
func ContainerManagerStop(c echo.Context, a *app.App) error {
	ID := c.Param("ID")
	// Stop the container gracefully with a timeout
	stopped, err := docker.StopContainerGracefully(ID, 3*time.Second)
	if err != nil {
		return sendError(c, err)
	}
	// if stopped is true, the container was stopped gracefully
	if stopped {
		return c.HTML(http.StatusOK, `Success`)
	}
	// If stopped is false, the container was not stopped gracefully we force stop it
	err = docker.StopContainerRagefully(ID)
	if err != nil {
		return sendError(c, err)
	}

	return c.HTML(http.StatusOK, `Success`)
}

// ContainerManagerStart handles the /container-manager/start route
func ContainerManagerStart(c echo.Context, a *app.App) error {
	ID := c.Param("ID")
	err := docker.StartContainer(ID)
	if err != nil {
		return sendError(c, err)
	}
	return c.HTML(http.StatusOK, `Success`)
}
