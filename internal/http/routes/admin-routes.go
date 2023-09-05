package routes

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/http/middlewares"
	"gogs.bnema.dev/gordon-echo/internal/ui"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func AdminRoute(c echo.Context) error {
	// Check if it's the first launch of the app
	if utils.IsConfigFilePresent() {
		fmt.Println("Fresh installation detected, redirecting to /admin/install")
		return c.Redirect(http.StatusFound, "/admin/install")
	}

	// Retrieve the current language from the context
	lang := c.Get(middlewares.LangKey).(string)

	staticData, err := utils.PopulateDataFromYAML(lang)
	if err != nil {
		return err
	}

	renderer, err := utils.GetRenderer("index.gohtml", ui.PublicFS, utils.NewLogger())
	if err != nil {
		return err
	}

	html, err := renderer.Render(staticData.CurrentLang)
	if err != nil {
		// Return the error into NewLogger() to log it
		utils.NewLogger().Error()
		return err
	}

	return c.HTML(http.StatusOK, html)
}

func InstallRoute(c echo.Context) error {
	// Check if it's the first launch of the app
	if !utils.IsConfigFilePresent() {
		fmt.Println("Config file already present, redirecting to /admin")
		return c.Redirect(http.StatusFound, "/admin")
	}

	// Retrieve the current language from the context
	lang := c.Get(middlewares.LangKey).(string)

	staticData, err := utils.PopulateDataFromYAML(lang)
	if err != nil {
		return err
	}

	renderer, err := utils.GetRenderer("install.gohtml", ui.PublicFS, utils.NewLogger())
	if err != nil {
		return err
	}

	html, err := renderer.Render(staticData.CurrentLang)
	if err != nil {
		// Return the error into NewLogger() to log it
		utils.NewLogger().Error()
		return err
	}

	return c.HTML(http.StatusOK, html)
}
