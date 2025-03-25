package handlers

import (
	"net/http"

	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/models/templ/pages/errors"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/labstack/echo/v4"
)

// RenderNotFoundPage renders a 404 error page using templ
func RenderNotFoundPage(c echo.Context, a *server.App) error {
	data := errors.ErrorPageData{
		Title:      "Page Not Found | Gordon",
		AdminPath:  a.Config.Admin.Path,
		StatusCode: http.StatusNotFound,
		Message:    "Sorry, the page you are looking for does not exist.",
	}
	
	renderer := render.NewTemplRenderer(a)
	return renderer.RenderTempl(c, errors.NotFoundPage(data))
}

// RenderForbiddenPage renders a 403 error page using templ
func RenderForbiddenPage(c echo.Context, a *server.App) error {
	data := errors.ErrorPageData{
		Title:      "Access Forbidden | Gordon",
		AdminPath:  a.Config.Admin.Path,
		StatusCode: http.StatusForbidden,
		Message:    "You don't have permission to access this resource.",
	}
	
	renderer := render.NewTemplRenderer(a)
	return renderer.RenderTempl(c, errors.ForbiddenPage(data))
} 