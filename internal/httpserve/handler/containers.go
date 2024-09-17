package handler

import (
	"fmt"
	"log"
	"net/http"
	"strings"
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
			log.Printf("Updated mapping: %s -> %s", key, value[0])
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
}

type HumanReadableContainer struct {
	*types.Container
	Name              string
	Ports             []string
	ShortID           string
	CreatedStr        string
	SizeStr           string
	UpSince           string
	StateColor        string
	TraefikEntryPoint string
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

	for _, img := range images {
		shortID := img.ID[7:19]
		safelyInteractWithIDMap(Update, shortID, img.ID)
		createdTime := time.Unix(img.Created, 0)
		createdStr := humanize.TimeAgo(createdTime)
		sizeStr := humanize.BytesToReadableSize(img.Size)

		for _, repoTag := range img.RepoTags {
			humanReadableImages = append(humanReadableImages, HumanReadableContainerImage{
				ID:          img.ID,
				ShortID:     shortID,
				CreatedStr:  createdStr,
				SizeStr:     sizeStr,
				Name:        repoTag,
				RepoDigests: img.RepoDigests,
				RepoTags:    img.RepoTags,
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
				Container:         &localContainer,
				SizeStr:           sizeStr,
				UpSince:           humanize.TimeAgo(time.Unix(container.Created, 0)),
				StateColor:        stateColor,
				Name:              name,
				Ports:             ports,
				TraefikEntryPoint: extractTraefikEntryPoint(container.Labels),
				CreatedStr:        humanize.TimeAgo(time.Unix(container.Created, 0)),
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

func extractTraefikEntryPoint(labels map[string]string) string {
	for key, value := range labels {
		if strings.HasSuffix(key, ".loadbalancer.server.port") {
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
