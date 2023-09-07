package routes

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/app"
	"gogs.bnema.dev/gordon-echo/internal/http/middlewares"
	"gogs.bnema.dev/gordon-echo/pkg/templating"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func AdminRoute(c echo.Context, ac *app.Config, a *app.App) error {
	// Check if it's the first launch of the app
	if utils.IsConfigFilePresent() {
		fmt.Println("Fresh installation detected, redirecting to /admin/install")
		return c.Redirect(http.StatusFound, "/admin/install")
	}

	// Retrieve the current language from the context
	lang := c.Get(middlewares.LangKey).(string)

	// Assuming the filename is "strings.yml"
	var data utils.StringsYamlData
	err := utils.PopulateDataFromYAML(lang, ac.GetTemplateFS(), "strings.yml", &data)
	if err != nil {
		return err
	}

	renderer, err := templating.GetHTMLRenderer("index.gohtml", ac.GetPublicFS(), a)
	if err != nil {
		return err
	}

	html, err := renderer.Render(data.CurrentLang, a)
	if err != nil {
		// Return the error into NewLogger() to log it
		utils.NewLogger().Error()
		return err
	}

	return c.HTML(http.StatusOK, html)
}

func InstallRoute(c echo.Context, ac *app.Config, a *app.App) error {
	// Check if it's the first launch of the app
	if !utils.IsConfigFilePresent() {
		fmt.Println("Config file already present, redirecting to /admin")
		return c.Redirect(http.StatusFound, "/admin")
	}

	// Retrieve the current language from the context
	lang := c.Get(middlewares.LangKey).(string)

	var data utils.StringsYamlData
	err := utils.PopulateDataFromYAML(lang, ac.GetTemplateFS(), "strings.yml", &data)
	if err != nil {
		return fmt.Errorf("failed to populate data from YAML: %w", err)
	}

	renderer, err := templating.GetHTMLRenderer("install.gohtml", ac.GetTemplateFS(), a)
	fmt.Println(renderer)
	if err != nil {
		return err
	}

	html, err := renderer.Render(data.CurrentLang, a)
	if err != nil {
		// Return the error into NewLogger() to log it
		utils.NewLogger().Error()
		return err
	}

	return c.HTML(http.StatusOK, html)
}
