package middlewares

import (
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

// HTTPAccessLogger creates a new middleware to log HTTP requests using the provided HttpLogger.
func HTTPAccessLogger(logger *utils.Logger) echo.MiddlewareFunc {
	return middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogURI:    true,
		LogStatus: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			// Filter out any requests that contain the substring "sse" in the URI
			if strings.Contains(v.URI, "sse") {
				return nil
			}
			logger.Info().
				Str("type", "http").
				Str("remote_ip", c.RealIP()).
				Str("method", c.Request().Method).
				Str("URI", v.URI).
				Int("status", v.Status).
				Msg("request")
			return nil
		},
	})
}

func SetLogger(logger *utils.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("logger", logger)
			return next(c)
		}
	}
}
