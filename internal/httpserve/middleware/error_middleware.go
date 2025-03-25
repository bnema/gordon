package middleware

import (
	"net/http"

	"github.com/bnema/gordon/internal/httpserve/handlers"
	"github.com/bnema/gordon/internal/server"
	"github.com/labstack/echo/v4"
)

// CustomHTTPErrorHandler is a custom error handler that uses templ to render error pages
func CustomHTTPErrorHandler(a *server.App) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		code := http.StatusInternalServerError
		if he, ok := err.(*echo.HTTPError); ok {
			code = he.Code
		}
		
		// Handle specific error codes with our templates
		switch code {
		case http.StatusNotFound:
			if err := handlers.RenderNotFoundPage(c, a); err != nil {
				c.Logger().Error(err)
			}
			return
		case http.StatusForbidden:
			if err := handlers.RenderForbiddenPage(c, a); err != nil {
				c.Logger().Error(err)
			}
			return
		default:
			// For other errors, use Echo's default error handler
			c.Echo().DefaultHTTPErrorHandler(err, c)
		}
	}
} 