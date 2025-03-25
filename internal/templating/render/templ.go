package render

import (
	"context"
	"fmt"
	"net/http"

	"github.com/a-h/templ"
	"github.com/bnema/gordon/internal/server"
	"github.com/labstack/echo/v4"
)

// Renderer for templ templates
type TemplRenderer struct {
	BuildVersion  string
	LatestVersion string
}

// RenderTempl renders a templ component
func (r *TemplRenderer) RenderTempl(ctx echo.Context, component templ.Component) error {
	// Set the content type and status code
	ctx.Response().Header().Set(echo.HeaderContentType, echo.MIMETextHTML)
	ctx.Response().WriteHeader(http.StatusOK)

	// Create a render context
	renderCtx := context.WithValue(ctx.Request().Context(), "BuildVersion", r.BuildVersion)
	
	// Render the component
	err := component.Render(renderCtx, ctx.Response().Writer)
	if err != nil {
		return fmt.Errorf("failed to render templ component: %w", err)
	}

	return nil
}

// NewTemplRenderer creates a new templ renderer
func NewTemplRenderer(a *server.App) *TemplRenderer {
	return &TemplRenderer{
		BuildVersion: a.Config.Build.BuildVersion,
	}
}