package handlers

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
)

func ErrorNumberHandler(err error, c echo.Context) {
	code := http.StatusInternalServerError
	if he, ok := err.(*echo.HTTPError); ok {
		code = he.Code
	}

	switch code {
	case http.StatusNotFound:
		c.String(code, fmt.Sprintf("An error occurred: %v", err))
	case http.StatusForbidden:
		c.String(code, fmt.Sprintf("An error occurred: %v", err))
	default:
		c.String(code, fmt.Sprintf("An error occurred: %v", err))
	}
}
