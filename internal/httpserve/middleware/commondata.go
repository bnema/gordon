package middleware

import (
	"github.com/bnema/gordon/internal/appserver"
	"github.com/labstack/echo/v4"
)

func SetCommonDataMiddleware(a *appserver.App) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// TODO: set common data
			return next(c)
		}
	}
}
