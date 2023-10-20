package handler

import (
	"fmt"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/httpserve/middleware"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/labstack/echo/v4"
)

var (
	mainPath      = "html/admin"
	fragmentsPath = "html/fragments"
)

func GetLocalizedData(c echo.Context, a *app.App) (map[string]interface{}, error) {
	lang := c.Get(middleware.LangKey)

	if lang == nil {
		return nil, fmt.Errorf("LangKey not found in context")
	}

	yamlData, err := render.GetLocalization(lang.(string), a)
	if err != nil {
		return nil, fmt.Errorf("failed to get localization: %w", err)
	}
	return map[string]interface{}{
		"Lang": yamlData,
	}, nil
}

// Handle the admin route to display index.gohtml from the templateFS with the data from strings.yml
func AdminRoute(c echo.Context, a *app.App) error {
	data, err := GetLocalizedData(c, a)
	if err != nil {
		return err
	}
	// Navigate inside the fs.FS to get the template
	rendererData, err := render.GetHTMLRenderer(mainPath, "index.gohtml", a.TemplateFS, a, fragmentsPath)
	if err != nil {
		return err
	}

	renderedHTML, err := rendererData.Render(data, a)
	if err != nil {
		return err
	}

	return c.HTML(200, renderedHTML)
}

func AdminManagerRoute(c echo.Context, a *app.App) error {
	data, err := GetLocalizedData(c, a)
	if err != nil {
		return err
	}

	// Navigate inside the fs.FS to get the template
	rendererData, err := render.GetHTMLRenderer(mainPath, "manager.gohtml", a.TemplateFS, a, fragmentsPath)
	if err != nil {
		return err
	}

	renderedHTML, err := rendererData.Render(data, a)
	if err != nil {
		return err
	}

	return c.HTML(200, renderedHTML)
}
