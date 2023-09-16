package middleware

import (
	"github.com/bnema/gordon/internal/app"
	"github.com/labstack/echo/v4"
)

func SetCommonDataMiddleware(a *app.App) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("BuildVersion", a.BuildVersion)
			// Add more common data if needed
			return next(c)
		}
	}
}
