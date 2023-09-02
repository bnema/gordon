package handlers

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/ui"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

type ErrorHandler struct {
	Err         error
	C           echo.Context
	ErrorNumber int
}

func Error404Handler(err error, c echo.Context) {
	if he, ok := err.(*echo.HTTPError); ok {
		if he.Code == http.StatusNotFound {
			renderer, err := utils.GetRenderer("404.gohtml", ui.PublicFS, utils.NewLogger())
			if err != nil {
				c.String(http.StatusInternalServerError, fmt.Sprintf("%v", err))
			}

			data := map[string]interface{}{
				"website": map[string]interface{}{
					"title": "Page Not Found",
				},
			}

			html, err := renderer.Render(data)
			if err != nil {
				c.String(http.StatusInternalServerError, fmt.Sprintf("%v", err))
			}

			c.HTML(http.StatusNotFound, html)
			return
		}
	}

}

func Error200Handler(err error, c echo.Context) {
	// Just print the error for now
	fmt.Println(err)
}

func Error403Handler(err error, c echo.Context) {
	// Just print the error for now
	fmt.Println(err)
}

func ErrorNumberHandler(err error, c echo.Context) {
	// For Each Error Code, we can have a different handler
}
