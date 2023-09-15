package handler

import (
	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/gotemplate/render"
	"github.com/bnema/gordon/internal/httpserve/middleware"
	"github.com/bnema/gordon/internal/webui"
	"github.com/labstack/echo/v4"
)

// Handle the admin route to display index.gohtml from the templateFS with the data from strings.yml
func AdminRoute(c echo.Context, a *app.App) error {
	lang := c.Get(middleware.LangKey).(string)
	yamlData := webui.StringsYamlData{}
	err := webui.ReadStringsDataFromYAML(lang, a.TemplateFS, "strings.yml", &yamlData)
	if err != nil {
		return err
	}

	// Navigate inside the fs.FS to get the template
	path := "html/admin"
	rendererData, err := render.GetHTMLRenderer(path, "index.gohtml", a.TemplateFS, a)
	if err != nil {
		return err
	}

	renderedHTML, err := rendererData.Render(yamlData.CurrentLang, a)
	if err != nil {
		return err
	}

	return c.HTML(200, renderedHTML)
}
