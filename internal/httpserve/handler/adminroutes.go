package handler

import (
	"fmt"
	"net/http"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/gotemplate/render"
	"github.com/bnema/gordon/internal/httpserve/middleware"
	"github.com/bnema/gordon/internal/webui"
	"github.com/bnema/gordon/pkg/utils/docker"
	"github.com/bnema/gordon/pkg/utils/sanitize"
	"github.com/labstack/echo/v4"
)

var (
	mainPath      = "html/admin"
	fragmentsPath = "html/fragments"
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
	rendererData, err := render.GetHTMLRenderer(mainPath, "index.gohtml", a.TemplateFS, a, fragmentsPath)
	if err != nil {
		return err
	}

	// Create a data map to pass to the renderer
	data := map[string]interface{}{
		"CurrentLang": yamlData.CurrentLang,
		// "BuildVersion" will be automatically added in the renderer
	}

	renderedHTML, err := rendererData.Render(data, a)
	if err != nil {
		return err
	}

	return c.HTML(200, renderedHTML)
}

func AdminManagerRoute(c echo.Context, a *app.App) error {
	lang := c.Get(middleware.LangKey).(string)
	yamlData := webui.StringsYamlData{}
	err := webui.ReadStringsDataFromYAML(lang, a.TemplateFS, "strings.yml", &yamlData)
	if err != nil {
		fmt.Printf("Error reading YAML: %v\n", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load language data")
	}

	// Navigate inside the fs.FS to get the template
	rendererData, err := render.GetHTMLRenderer(mainPath, "manager.gohtml", a.TemplateFS, a, fragmentsPath)
	if err != nil {
		return err
	}

	images, err := docker.ListContainerImages()
	if err != nil {
		rawErrHTML := `<div id="container-images">Error: ` + err.Error() + `</div>`
		sanitizedHTML, err := sanitize.SanitizeHTML(rawErrHTML)
		if err != nil {
			// Handle the sanitization error
			return c.String(http.StatusInternalServerError, "An error occurred during sanitization")
		}
		return c.HTML(http.StatusInternalServerError, sanitizedHTML)
	}

	// Create a data map to pass to the renderer
	data := map[string]interface{}{
		"CurrentLang": yamlData.CurrentLang,
		"Images":      images,
	}

	renderedHTML, err := rendererData.Render(data, a)
	if err != nil {
		return err
	}

	return c.HTML(200, renderedHTML)
}
