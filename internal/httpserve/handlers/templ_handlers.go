package handlers

import (
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/models/templ/components"
	"github.com/bnema/gordon/internal/templating/models/templ/pages/admin"
	"github.com/bnema/gordon/internal/templating/models/templ/pages/login"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/docker/docker/api/types"
	"github.com/labstack/echo/v4"
	"strings"
	"time"
	"github.com/bnema/gordon/pkg/humanize"
	"net/http"
)

// RenderTemplLoginPage renders the login page using templ
func RenderTemplLoginPage(c echo.Context, a *server.App) error {
	// Check for token in query param for backward compatibility
	urlToken := c.QueryParam("token")
	_ = urlToken // Not using this yet, but kept for compatibility

	// Check for error message
	errorMsg := c.QueryParam("error")

	// Create a templ renderer
	renderer := render.NewTemplRenderer(a)

	// Render the login page
	return renderer.RenderTempl(c, login.LoginPage("Login", a.Config.Build.BuildVersion, errorMsg))
}

// RenderTemplDashboardPage renders the dashboard page using templ
func RenderTemplDashboardPage(c echo.Context, a *server.App) error {
	// Create a templ renderer
	renderer := render.NewTemplRenderer(a)

	// Get user-specific URLs
	adminPath := a.Config.Admin.Path
	userSettingsURL := adminPath + "/settings"
	logoutURL := adminPath + "/logout"

	// Render the dashboard page
	return renderer.RenderTempl(c, admin.DashboardPage(
		"Dashboard", 
		a.Config.Build.BuildVersion, 
		adminPath, 
		userSettingsURL, 
		logoutURL,
	))
}

// RenderTemplManagerPage renders the manager page using templ
func RenderTemplManagerPage(c echo.Context, a *server.App) error {
	// Create a templ renderer
	renderer := render.NewTemplRenderer(a)

	// Get container and image counts
	containerCount, _ := GetContainerCount(a)
	imageCount, _ := GetImageCount(a)

	// Get user-specific URLs
	adminPath := a.Config.Admin.Path
	userSettingsURL := adminPath + "/settings"
	logoutURL := adminPath + "/logout"

	// Render the manager page
	return renderer.RenderTempl(c, admin.ManagerPage(
		"Container Manager", 
		a.Config.Build.BuildVersion, 
		adminPath, 
		userSettingsURL, 
		logoutURL,
		containerCount,
		imageCount,
	))
}

// RenderTemplContainerList renders the container list component
func RenderTemplContainerList(c echo.Context, a *server.App) error {
	// Create a templ renderer
	renderer := render.NewTemplRenderer(a)

	// Get the containers
	containers, noContainer, err := GetContainers(a)
	if err != nil {
		return err
	}

	// Render the container list component
	return renderer.RenderTempl(c, components.ContainerList(containers, noContainer))
}

// RenderTemplImageList renders the image list component
func RenderTemplImageList(c echo.Context, a *server.App) error {
	// Create a templ renderer
	renderer := render.NewTemplRenderer(a)

	// Get the images
	images, err := docker.ListContainerImages()
	if err != nil {
		return err
	}

	// Convert to human-readable format
	var humanReadableImages []components.HumanReadableImage
	for _, img := range images {
		shortID := img.ID[7:19]
		
		for _, repoTag := range img.RepoTags {
			// Split the repo tag into repository and tag
			parts := strings.Split(repoTag, ":")
			repository := parts[0]
			tag := "latest"
			if len(parts) > 1 {
				tag = parts[1]
			}

			humanReadableImages = append(humanReadableImages, components.HumanReadableImage{
				ID:          img.ID,
				ShortID:     shortID,
				Name:        repoTag,
				Repository:  repository,
				Tag:         tag,
				Size:        humanize.BytesToReadableSize(img.Size),
				Created:     humanize.TimeAgo(time.Unix(img.Created, 0)),
				RepoDigests: img.RepoDigests,
				RepoTags:    img.RepoTags,
			})
		}
	}

	// Render the image list component
	return renderer.RenderTempl(c, components.ImageList(humanReadableImages))
}

// RenderTemplUploadImageForm renders the upload image form component
func RenderTemplUploadImageForm(c echo.Context, a *server.App) error {
	// Create a templ renderer
	renderer := render.NewTemplRenderer(a)

	// Render the upload image form component
	return renderer.RenderTempl(c, components.UploadImageForm())
}

// RenderTemplCreateContainerForm renders the create container form component
func RenderTemplCreateContainerForm(c echo.Context, a *server.App) error {
	// Create a templ renderer
	renderer := render.NewTemplRenderer(a)

	// Get the image ID from the URL parameter
	shortID := c.Param("ID")
	
	// Try to fetch the image info
	imageID, exists := safelyInteractWithIDMap(Fetch, shortID)
	if !exists {
		return c.String(http.StatusBadRequest, "Invalid image ID")
	}
	
	// Get image info
	image, err := docker.GetImageInfo(imageID)
	if err != nil {
		return err
	}
	
	// Prepare image name from repo tags
	imageName := "unknown"
	if len(image.RepoTags) > 0 {
		imageName = image.RepoTags[0]
	}
	
	// Prepare form data
	formData := components.ContainerFormData{
		ImageName: imageName,
		ImageID:   imageID,
		ShortID:   shortID,
		// Default values can be set here if needed
		ProxyPort: "80", // Default proxy port
	}
	
	// Render the create container form component
	return renderer.RenderTempl(c, components.CreateContainerForm(formData))
}

// GetContainerCount returns the count of containers
func GetContainerCount(a *server.App) (int, error) {
	containers, err := docker.ListRunningContainers()
	if err != nil {
		return 0, err
	}
	return len(containers), nil
}

// GetImageCount returns the count of images
func GetImageCount(a *server.App) (int, error) {
	images, err := docker.ListContainerImages()
	if err != nil {
		return 0, err
	}
	return len(images), nil
}

// GetContainers returns the list of containers
func GetContainers(a *server.App) ([]types.Container, bool, error) {
	containers, err := docker.ListRunningContainers()
	if err != nil {
		return nil, false, err
	}
	noContainer := len(containers) == 0
	return containers, noContainer, nil
}