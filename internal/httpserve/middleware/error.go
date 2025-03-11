package middleware

import (
	"github.com/bnema/gordon/pkg/logger"
	"github.com/labstack/echo/v4"
)

func ErrorHandler(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		err := next(c)
		if err != nil {
			logger.Error("Caught http error",
				"path", c.Request().URL.Path,
				"method", c.Request().Method,
				"error", err,
				"status", c.Response().Status)
		}
		return err
	}
}
