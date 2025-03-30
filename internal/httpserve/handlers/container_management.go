package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/load"
	components "github.com/bnema/gordon/internal/templating/models/templ/components"
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
	images, err := docker.ListContainerImages()
	if err != nil {
		return sendError(c, err)
	}

	var humanReadableImages []components.HumanReadableImage
	for _, img := range images {
		shortID := img.ID[7:19] // Display a shorter version of the ID
		safelyInteractWithIDMap(Update, shortID, img.ID)
		createdStr := humanize.TimeAgo(time.Unix(img.Created, 0))
		sizeStr := humanize.BytesToReadableSize(img.Size)

		// Parse RepoTags to extract repository and tag
		var repository, tag string
		repoTag := "<none>:<none>"
		if len(img.RepoTags) > 0 {
			repoTag = img.RepoTags[0]
			parts := strings.Split(repoTag, ":")
			if len(parts) == 2 {
				repository = parts[0]
				tag = parts[1]
			} else {
				repository = repoTag // Handle cases like "image" without a tag
				tag = "latest"
			}
		}

		// Create instance of components.HumanReadableImage
		humanReadableImages = append(humanReadableImages, components.HumanReadableImage{
			ID:          img.ID,
			ShortID:     shortID,
			Created:     createdStr,
			Size:        sizeStr,
			Name:        repoTag,
			RepoDigests: img.RepoDigests,
			RepoTags:    img.RepoTags,
			Repository:  repository,
			Tag:         tag,
		})
	}

	logger.Debug("Rendering imagelist template with", len(humanReadableImages), "images")

	// Use the Templ renderer
	renderer := render.NewTemplRenderer(a)
	component := components.ImageList(humanReadableImages)

	return renderer.RenderTempl(c, component)
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

// ImageManagerPrune handles the /image-manager/prune route
func ImageManagerPrune(c echo.Context, a *server.App) error {
	numPurged, err := docker.PruneImages()
	if err != nil {
		return sendError(c, err)
	}
	return c.String(http.StatusOK, fmt.Sprintf("Successfully purged %d images", numPurged))
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

	// Use the Templ renderer
	renderer := render.NewTemplRenderer(a)
	component := components.ContainerList(containers, len(containers) == 0)

	return renderer.RenderTempl(c, component)
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
