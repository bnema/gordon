package httpserve

import (
	"github.com/bnema/gordon/internal/app"
	"github.com/labstack/echo/v4"
)

func RegisterRoutes(e *echo.Echo, a *app.App) *echo.Echo {
	AdminPath := a.AdminPath
	// Hello world on the admin path
	e.GET(AdminPath, helloWorld)
	return e
}

func helloWorld(c echo.Context) error {
	return c.String(200, "Hello world")
}
