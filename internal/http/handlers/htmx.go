package handlers

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/http/middlewares"
	"gogs.bnema.dev/gordon-echo/pkg/htmx"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func HTMXHandler(c echo.Context) error {
	// Get the logger instance
	logger := utils.NewLogger().GetTypeLogger(utils.App)
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
		return htmxFragmentHandler(c, logger)
	}

	// For other HTTP methods, return an error
	return c.String(http.StatusBadRequest, "Invalid request method")
}

func htmxFragmentHandler(c echo.Context, logger *utils.Logger) error {
	fragment := c.Request().Header.Get("HX-Fragment")
	if fragment == "" {
		logger.Error().Msg("Missing fragment header")
		return c.String(http.StatusBadRequest, "Missing fragment header")
	}

	lang := c.Get(middlewares.LangKey).(string)
	staticData, err := utils.PopulateDataFromYAML(lang)
	if err != nil {
		return err
	}

	content, err := utils.GetHTMLFragmentByID(fragment, staticData.CurrentLang)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get fragment")
		return c.String(http.StatusInternalServerError, err.Error())
	}
	return c.HTML(http.StatusOK, content)
}
