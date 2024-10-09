package handlers

import (
	"fmt"

	"net/http"

	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/labstack/echo/v4"
)

var (
	mainPath      = "html/admin"
	fragmentsPath = "html/fragments"
)

// GetLocalizedData returns the data for the localization
func GetLocalizedData(c echo.Context, a *server.App) (map[string]interface{}, error) {
	lang := c.Get("LangKey")
	if lang == nil {
		return nil, fmt.Errorf("LangKey not found in context")
	}

	yamlData, err := render.GetLocalization(lang.(string), a)
	if err != nil {
		return nil, fmt.Errorf("failed to get localization: %w", err)
	}
	return map[string]interface{}{
		"Lang":      yamlData,
		"LogoutURL": a.Config.Admin.Path + "/logout",
		"AdminPath": a.Config.Admin.Path,
	}, nil
}

// renderAdminPage is a generalized function to render admin pages
func renderAdminPage(c echo.Context, a *server.App, templateName string) error {
	data, err := GetLocalizedData(c, a)
	if err != nil {
		return err
	}

	data["AdminPath"] = a.Config.Admin.Path
	// DEBUG show the admin path here
	fmt.Println("AdminPath:", a.Config.Admin.Path)

	rendererData, err := render.GetHTMLRenderer(mainPath, templateName, a.TemplateFS, a, fragmentsPath)
	if err != nil {
		return err
	}

	renderedHTML, err := rendererData.Render(data, a)
	if err != nil {
		return err
	}

	return c.HTML(http.StatusOK, renderedHTML)
}

// AdminRoute is the route for the admin panel
func AdminRoute(c echo.Context, a *server.App) error {
	return renderAdminPage(c, a, "index.gohtml")
}

// AdminManagerRoute is the route of the manager page
func AdminManagerRoute(c echo.Context, a *server.App) error {
	return renderAdminPage(c, a, "manager.gohtml")
}
