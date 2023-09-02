package handlers

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/ui"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func DefaultHTTPErrorHandler(err error, c echo.Context) {
	renderer, err := utils.GetRenderer("500.gohtml", ui.PublicFS)
	if err != nil {
		c.String(http.StatusInternalServerError, fmt.Sprintf("%v", err))
	}

	data := map[string]interface{}{
		"website": map[string]interface{}{
			"title": "Internal Server Error",
		},
	}

	html, err := renderer.Render(data)
	if err != nil {
		c.String(http.StatusInternalServerError, fmt.Sprintf("%v", err))
	}

	c.HTML(http.StatusInternalServerError, html)
}
