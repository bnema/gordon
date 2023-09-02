package handlers

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/pkg/htmx"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func HTMXHandler(c echo.Context) error {
	// Detect and extract HTMX data from the request
	htmxRequest := htmx.GetRequest(c)

	// If the request is not an HTMX request, return an error
	if !htmxRequest.Enabled {
		return c.String(http.StatusBadRequest, "Non-HTMX request")
	}

	// If it's a GET request, handle the fragment
	if c.Request().Method == http.MethodGet {
		return HTMXFragmentHandler(c)
	}

	// For other HTTP methods, return an error

	return c.String(http.StatusBadRequest, "Invalid request method")
}

func HTMXFragmentHandler(c echo.Context) error {
	fragment := c.Request().Header.Get("X-Fragment")
	if fragment == "" {
		return c.String(http.StatusBadRequest, "Missing fragment header")
	}

	content, err := utils.GetHTMLFragmentByID(fragment)
	if err != nil {
		// You might want to differentiate between different types of errors:
		// 1. If the fragment doesn't match any known component (return a 404 error)
		// 2. If there's another error while fetching the component (return a 500 error)
		// For this example, I'm treating all errors as internal server errors for simplicity.
		return c.String(http.StatusInternalServerError, err.Error())
	}

	// Create and apply an HTMX response if necessary
	// htmxResponse := htmx.Response{Refresh: true}
	// htmxResponse.Apply(c)

	return c.HTML(http.StatusOK, content)
}
