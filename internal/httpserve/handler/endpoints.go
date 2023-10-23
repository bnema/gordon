package handler

import (
	"net/http"

	"github.com/bnema/gordon/internal/app"
	"github.com/labstack/echo/v4"
)

// Handle GET on /api endpoint

func GetHello(c echo.Context, a *app.App) error {
	return c.JSON(http.StatusOK, "Hello, World!")
}
