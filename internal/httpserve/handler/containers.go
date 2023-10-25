package handler

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/load"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/humanize"
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

// renderHTML is a generalized function to render HTML
func renderHTML(c echo.Context, a *server.App, path, templateName string, data map[string]interface{}) error {
	rendererData, err := render.GetHTMLRenderer(path, templateName, a.TemplateFS, a)
	if err != nil {
		return err
	}
	renderedHTML, err := rendererData.Render(data, a)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, renderedHTML)
}

// ActionSuccess returns the success HTML fragment
func ActionSuccess(a *server.App) string {
	successFragment, err := load.Fragment(a, "success")
	if err != nil {
		fmt.Println(err)
		return ""
	}

	return successFragment
}

// ImageManagerComponent handles the /image-manager route (HTMX route)
func ImageManagerComponent(c echo.Context, a *server.App) error {
	images, err := docker.ListContainerImages()
	if err != nil {
		return sendError(c, err)
	}
	var humanReadableImages []HumanReadableContainerImage

	for _, image := range images {
		// We need to shorten the ID because it breaks the HTML
		ShortID := image.ID[7:19]
		// And we store both the ShortID and the full ID in a map so we can retrieve the full ID later
		safelyInteractWithIDMap(Update, ShortID, image.ID)
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

	return renderHTML(c, a, "html/fragments", "imagelist.gohtml", data)
}

// ImageManagerDelete handles the /image-manager/delete route
func ImageManagerDelete(c echo.Context, a *server.App) error {
	//
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
	containers, err := docker.ListRunningContainers()
	if err != nil {
		return sendError(c, err)
	}

	var humanReadableContainers []HumanReadableContainer

	for _, container := range containers {
		localContainer := container // Make a local copy
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
				SizeStr:    sizeStr,
				UpSince:    humanize.TimeAgo(time.Unix(container.Created, 0)),
				StateColor: stateColor,
				Name:       name,
				Ports:      ports,
			})
		}
	}
	yamlData, err := GetLocalizedData(c, a)
	if err != nil {
		return err
	}
	data := map[string]interface{}{
		"Lang":       yamlData["Lang"],
		"containers": humanReadableContainers,
	}

	return renderHTML(c, a, "html/fragments", "containerlist.gohtml", data)
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
