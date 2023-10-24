package middleware

import (
	"fmt"
	"strings"

	"github.com/bnema/gordon/internal/app"
	"github.com/labstack/echo/v4"
)

// RequireToken is a middleware that checks for a valid token in the request
func RequireToken(a *app.App) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if err := validateToken(c, a); err != nil {
				return err
			}
			return next(c)
		}
	}
}

func validateToken(c echo.Context, a *app.App) error {
	token := c.Request().Header.Get("Authorization")
	if token == "" {
		return echo.ErrUnauthorized
	}
	fmt.Println(token)
	token = strings.Replace(token, "Bearer ", "", 1)
	configToken, err := a.Config.GetToken()
	if err != nil {
		return fmt.Errorf("failed to get token from config: %w", err)
	}

	if token != configToken {
		return echo.ErrUnauthorized
	}

	return nil
}