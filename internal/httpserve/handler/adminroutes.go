package handler

import (
	"github.com/labstack/echo/v4"
)

// Handle the admin route to display index.gohtml from the templateFS with the data from strings.yaml
func AdminRoute(c echo.Context) error {
	return c.Render(200, "index.gohtml", c.Get("data"))
}
