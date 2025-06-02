package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/server"
	components "github.com/bnema/gordon/internal/templating/models/templ/components"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/humanize"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/docker/docker/api/types"
	"github.com/labstack/echo/v4"
)

// Shared state for last prune result
var lastPruneResult struct {
	Done    bool
	Success bool
	Message string
	mutex   sync.Mutex
}

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

	// return just a string
	return "success"
}

// ImageManagerComponent handles the /image-manager route (HTMX route)
func ImageManagerComponent(c echo.Context, a *server.App) error {
	// Check last prune status and add trigger header if needed
	lastPruneResult.mutex.Lock()
	if lastPruneResult.Done {
		var level string
		if lastPruneResult.Success {
			level = "success"
		} else {
			level = "error"
		}
		// Prepare JSON payload for HX-Trigger
		payload := map[string]string{"level": level, "message": lastPruneResult.Message}
		jsonPayload, _ := json.Marshal(payload) // Error handling omitted for brevity
		c.Response().Header().Set("HX-Trigger", fmt.Sprintf(`{"showMessage": %s}`, string(jsonPayload)))

		// Reset the flag
		lastPruneResult.Done = false
	}
	lastPruneResult.mutex.Unlock()

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
	// Run pruning in a background goroutine
	go func() {
		var success bool
		var message string
		logger.Info("Starting background image prune...")
		numPurged, err := docker.PruneImages() // Remove context argument
		if err != nil {
			logger.Error("Background image prune failed", "error", err)
			success = false
			message = fmt.Sprintf("Image prune failed: %v", err)
			// Optionally, notify the user or admin about the failure (e.g., via websocket, SSE, or a status endpoint)
			return
		} else {
			logger.Info("Background image prune completed", "purged_count", numPurged)
			success = true
			message = fmt.Sprintf("Successfully purged %d images.", numPurged)
			// Optionally, notify the user about completion (e.g., via websocket, SSE, or updating the UI)
		}

		// Update shared state
		lastPruneResult.mutex.Lock()
		lastPruneResult.Done = true
		lastPruneResult.Success = success
		lastPruneResult.Message = message
		lastPruneResult.mutex.Unlock()
	}()

	// Immediately return a placeholder that initiates polling
	// Note: We need to modify imagelist.templ to handle this response properly
	// For now, just return 202 Accepted and indicate work started.
	// The actual polling trigger will be added in the template.
	c.Response().WriteHeader(http.StatusAccepted)
	// We'll return a small snippet that the template swap can use
	// This snippet should include the polling mechanism itself.
	// Example (needs refinement in the template):
	return c.HTML(http.StatusAccepted, `<div id="prune-status-poller" hx-get="/htmx/image-manager/prune-status" hx-trigger="load, every 3s" hx-target="#prune-status-poller" hx-swap="outerHTML">Pruning images in background... Checking status.</div>`)
}

// ImageManagerPruneStatus checks the status of a background prune operation
func ImageManagerPruneStatus(c echo.Context, a *server.App) error {
	lastPruneResult.mutex.Lock()
	defer lastPruneResult.mutex.Unlock()

	// If prune is not done, return an empty response or keep the poller active
	if !lastPruneResult.Done {
		// Returning 204 No Content will stop polling if using hx-swap="none"
		// However, our poller replaces itself, so we just return the same polling div
		// or an updated status message within it.
		// Let's keep the existing poller HTML but maybe update the text slightly.
		return c.HTML(http.StatusOK, `<div id="prune-status-poller" hx-get="/htmx/image-manager/prune-status" hx-trigger="every 3s" hx-target="#prune-status-poller" hx-swap="outerHTML">Still pruning... Checking status.</div>`)
	}

	// Prune is done, prepare the final response
	logger.Info("Prune complete, preparing final response for poller.")

	// Prepare JSON payload for HX-Trigger toast message
	var level string
	if lastPruneResult.Success {
		level = "success"
	} else {
		level = "error"
	}
	payload := map[string]string{"level": level, "message": lastPruneResult.Message}
	jsonPayload, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		logger.Error("Failed to marshal prune result payload for poller", "error", marshalErr)
		// Handle marshalling error - maybe return an error component
	} else {
		c.Response().Header().Set("HX-Trigger", fmt.Sprintf(`{"showMessage": %s}`, string(jsonPayload)))
	}

	// Reset the flag *after* preparing the response
	lastPruneResult.Done = false
	message := lastPruneResult.Message // Store message before resetting if needed
	success := lastPruneResult.Success
	// Reset message/success if needed, or maybe they are only relevant for the toast
	// lastPruneResult.Message = ""
	// lastPruneResult.Success = false

	// Fetch the updated image list (copy logic from ImageManagerComponent)
	images, listErr := docker.ListContainerImages()
	if listErr != nil {
		logger.Error("Failed to list images after prune for poller", "error", listErr)
		// Return an error FRAGMENT instead of a full page.
		// HX-Trigger for the prune status itself was already set.
		pruneStatusMsg := "Prune finished but failed to refresh list."
		if !success { // Add prune status detail if it also failed
			pruneStatusMsg = fmt.Sprintf("Prune status: %s. Failed to refresh list.", message)
		}
		return c.HTML(http.StatusOK, fmt.Sprintf("<div class='alert alert-error'>%s Error: %v. Please refresh manually.</div>", pruneStatusMsg, listErr))
	}

	var humanReadableImages []components.HumanReadableImage
	for _, img := range images {
		shortID := img.ID[7:19]
		safelyInteractWithIDMap(Update, shortID, img.ID)
		createdStr := humanize.TimeAgo(time.Unix(img.Created, 0))
		sizeStr := humanize.BytesToReadableSize(img.Size)
		var repository, tag string
		repoTag := "<none>:<none>"
		if len(img.RepoTags) > 0 {
			repoTag = img.RepoTags[0]
			parts := strings.Split(repoTag, ":")
			if len(parts) == 2 {
				repository = parts[0]
				tag = parts[1]
			} else {
				repository = repoTag
				tag = "latest"
			}
		}
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

	// Render the final ImageList component to replace the poller
	renderer := render.NewTemplRenderer(a)
	component := components.ImageList(humanReadableImages)
	logger.Debug("Rendering final ImageList for poller")
	return renderer.RenderTempl(c, component)
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
