package routes

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/ui"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func AdminRoute(c echo.Context) error {
	staticData, err := utils.LoadDataFromYAML()
	fmt.Println(staticData)
	if err != nil {
		return err
	}

	lang := c.Param("lang")

	switch lang {
	case "fr":
		staticData.CurrentLang = staticData.FR
	default: // default to English if no match
		staticData.CurrentLang = staticData.EN
	}

	renderer, err := utils.GetRenderer("index.gohtml", ui.PublicFS)
	if err != nil {
		return err
	}

	html, err := renderer.Render(staticData)
	if err != nil {
		return c.String(http.StatusInternalServerError, fmt.Sprintf("%v", err))
	}

	return c.HTML(http.StatusOK, html)
}

func StaticRoute(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "public, max-age=86400")
	return echo.StaticDirectoryHandler(ui.PublicFS, false)(c)
}
