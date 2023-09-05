package handlers

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/config"
	"gogs.bnema.dev/gordon-echo/internal/http/middlewares"
	"gogs.bnema.dev/gordon-echo/pkg/htmx"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func HTMXHandler(c echo.Context) error {
	logger, ok := c.Get("logger").(*utils.Logger)
	if !ok {
		return c.String(http.StatusInternalServerError, "Failed to get logger from context")
	}

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
		cfg, ok := c.Get("config").(config.Provider)
		if !ok {
			logger.Error().Msg("Failed to get config provider from context")
			return c.String(http.StatusInternalServerError, "Failed to get config provider from context")
		}
		return htmxFragmentHandler(c, logger, cfg)
	}

	// For other HTTP methods, return an error
	return c.String(http.StatusBadRequest, "Invalid request method")
}

func htmxFragmentHandler(c echo.Context, logger *utils.Logger, fs config.Provider) error {
	fragment := c.Request().Header.Get("HX-Fragment")
	if fragment == "" {
		logger.Error().Msg("Missing fragment header")
		return c.String(http.StatusBadRequest, "Missing fragment header")
	}

	lang := c.Get(middlewares.LangKey).(string)
	var data utils.StringsYamlData
	err := utils.PopulateDataFromYAML(lang, fs.GetTemplateFS(), "strings.yml", &data)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to populate data from YAML")
		return err
	}

	content, err := utils.GetHTMLFragmentByID(fragment, data.CurrentLang, fs)
	if err != nil { // <-- This check was missing
		logger.Error().Err(err).Msg("Failed to get fragment")
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return c.HTML(http.StatusOK, content)
}
