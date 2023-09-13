package handler

import (
	"fmt"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/gotemplate/render"
	"github.com/bnema/gordon/internal/httpserve/middleware"
	"github.com/bnema/gordon/internal/webui"
	"github.com/labstack/echo/v4"
)

// Handle the admin route to display index.gohtml from the templateFS with the data from strings.yml
func AdminRoute(c echo.Context, a *app.App) error {
	lang := c.Get(middleware.LangKey).(string)
	fmt.Println(lang)
	data := webui.StringsYamlData{}
	err := webui.ReadStringsDataFromYAML(lang, a.TemplateFS, "strings.yml", &data)
	if err != nil {
		return fmt.Errorf("failed to read strings data from YAML: %w", err)
	}

	// Navigate inside the fs.FS to get the template

	rendererData, err := render.GetHTMLRenderer("html/admin/index.gohtml", a.TemplateFS, a)
	if err != nil {
		return fmt.Errorf("failed to get renderer: %w", err)
	}

	renderedHTML, err := rendererData.Render(data, a)
	fmt.Println(renderedHTML)
	if err != nil {
		return fmt.Errorf("failed to render template: %w", err)
	}

	return c.HTML(200, renderedHTML)
}
