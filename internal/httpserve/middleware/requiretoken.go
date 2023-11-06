package middleware

import (
	"fmt"
	"strings"

	"github.com/bnema/gordon/internal/server"
	"github.com/labstack/echo/v4"
)

// RequireToken is a middleware that checks for a valid token in the request
func RequireToken(a *server.App) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if err := validateToken(c, a); err != nil {
				return err
			}
			return next(c)
		}
	}
}

func validateToken(c echo.Context, a *server.App) error {
	token := c.Request().Header.Get("Authorization")
	if token == "" {
		return c.JSON(401, "Unauthorized")
	}
	fmt.Println(token)
	token = strings.Replace(token, "Bearer ", "", 1)
	configToken, err := a.Config.GetToken()
	if err != nil {
		return fmt.Errorf("failed to get token from config: %w", err)
	}

	if token != configToken {
		return c.JSON(401, "Unauthorized")
	}

	return nil
}
