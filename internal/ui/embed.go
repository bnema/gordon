package ui

import (
	"embed"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

// go embed all:public
var public embed.FS

// go embed all:templates
var templates embed.FS

var PublicFS = echo.MustSubFS(public, "public")
var TemplateFS = echo.MustSubFS(templates, "templates")

// Render renders a template with the specified data and writes to the response.
func Render(c echo.Context, name string, data any) {
	r := &utils.Renderer{}
	return r.Render(c, name, data)
}
