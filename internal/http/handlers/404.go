package handlers

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/ui"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func Custom404Handler(err error, c echo.Context) {
	if he, ok := err.(*echo.HTTPError); ok {
		if he.Code == http.StatusNotFound {
			renderer, err := utils.GetRenderer("404.gohtml", ui.PublicFS)
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

	DefaultHTTPErrorHandler(err, c)
}
