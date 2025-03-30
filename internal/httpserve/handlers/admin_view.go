package handlers

import (
	"fmt"

	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/models/templ/pages/admin"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/labstack/echo/v4"
)

// GetLocalizedData returns the data for the localization
func GetLocalizedData(c echo.Context, a *server.App) (map[string]string, error) {
	lang := c.Get("LangKey")
	if lang == nil {
		return nil, fmt.Errorf("LangKey not found in context")
	}

	langStr, ok := lang.(string)
	if !ok {
		return nil, fmt.Errorf("LangKey is not a string")
	}

	yamlDataInterface, err := render.GetLocalization(langStr, a)
	if err != nil {
		return nil, fmt.Errorf("failed to get localization: %w", err)
	}

	// Convert map[string]interface{} to map[string]string
	langContent := make(map[string]string)
	for key, value := range yamlDataInterface {
		strValue, ok := value.(string)
		if !ok {
			// Handle cases where the value is not a string, e.g., log a warning or skip
			// For now, we'll skip non-string values
			continue
		}
		langContent[key] = strValue
	}

	// Return map[string]string directly
	return langContent, nil
}

// AdminRoute is the route for the admin panel
func AdminRoute(c echo.Context, a *server.App) error {
	langContent, err := GetLocalizedData(c, a)
	if err != nil {
		return sendError(c, err) // Assuming sendError exists
	}

	data := admin.IndexPageData{
		Title:        "Admin Dashboard", // Or get from LangContent if available
		BuildVersion: a.Config.Build.BuildVersion,
		AdminPath:    a.Config.Admin.Path,
		UserSettings: a.Config.Admin.Path + "/user", // Assuming this path
		LogoutURL:    a.Config.Admin.Path + "/logout",
		LangContent:  langContent,
	}

	// Use the Templ renderer
	renderer := render.NewTemplRenderer(a)
	component := admin.IndexPage(data)

	return renderer.RenderTempl(c, component)
}

// AdminManagerRoute is the route of the manager page
func AdminManagerRoute(c echo.Context, a *server.App) error {
	// Fetch container and image counts
	containers, err := docker.ListRunningContainers()
	if err != nil {
		return sendError(c, fmt.Errorf("failed to list containers: %w", err))
	}
	images, err := docker.ListContainerImages()
	if err != nil {
		return sendError(c, fmt.Errorf("failed to list images: %w", err))
	}

	containerCount := len(containers)
	imageCount := len(images)

	title := "Container Manager"                     // Set a default title
	userSettingsURL := a.Config.Admin.Path + "/user" // Assuming this path
	logoutURL := a.Config.Admin.Path + "/logout"

	// Use the Templ renderer
	renderer := render.NewTemplRenderer(a)
	component := admin.ManagerPage(
		title,
		a.Config.Build.BuildVersion,
		a.Config.Admin.Path,
		userSettingsURL,
		logoutURL,
		containerCount,
		imageCount,
	)

	return renderer.RenderTempl(c, component)
}
