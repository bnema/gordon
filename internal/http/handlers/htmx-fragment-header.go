package handlers

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/app"
	"gogs.bnema.dev/gordon-echo/internal/http/middlewares"
	"gogs.bnema.dev/gordon-echo/pkg/htmx"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func HTMXHandler(c echo.Context, a *app.App, ac *app.Config) error {
	logger := a.AppLogger
	// Detect and extract HTMX data from the request
	htmxRequest, err := htmx.GetRequest(c)
	if err != nil {
		logger.Debug().Err(err).Msg("Failed to extract HTMX data")
		return err
	}

	// If the request is not an HTMX request, return an error
	if !htmxRequest.Enabled {
		logger.Error().Msg("Request is not an HTMX request")
		return c.String(http.StatusBadRequest, "Not an HTMX request")
	}

	// If it's a GET request, handle the fragment
	if c.Request().Method == http.MethodGet {
		return htmxFragmentHandler(c, a, ac)
	}

	// For other HTTP methods, return an error
	return c.String(http.StatusBadRequest, "Invalid request method")
}

func htmxFragmentHandler(c echo.Context, a *app.App, ac *app.Config) error {
	logger := a.AppLogger
	fragment := c.Request().Header.Get("HX-Fragment")
	if fragment == "" {
		logger.Error().Msg("Missing fragment header")
		return c.String(http.StatusBadRequest, "Missing fragment header")
	}

	lang := c.Get(middlewares.LangKey).(string)
	var data utils.StringsYamlData
	err := utils.PopulateDataFromYAML(lang, ac.GetTemplateFS(), "strings.yml", &data)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to populate data from YAML")
		return err
	}

	content, err := htmx.GetHTMLFragmentByID(fragment, data.CurrentLang, a, ac)
	if err != nil { // <-- This check was missing
		logger.Error().Err(err).Msg("Failed to get fragment")
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return c.HTML(http.StatusOK, content)
}
