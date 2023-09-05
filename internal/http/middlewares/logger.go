package middlewares

import (
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/rs/zerolog"
)

func NewRequestLogger(logger zerolog.Logger) echo.MiddlewareFunc {
	return middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogURI:    true,
		LogStatus: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
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
