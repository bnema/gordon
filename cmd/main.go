package main

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/ui"
)

func main() {
	e := echo.New()

	e.GET("/", func(c echo.Context) error {
		// render the internal/ui/templates/index.html template
		return ui.Render(c, "index.html", nil)
	})

	e.GET("/public/*", echo.WrapHandler(http.FileServer(http.FS(ui.PublicFS))))

	e.Logger.Fatal(e.Start(":1323"))

}
