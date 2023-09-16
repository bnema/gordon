package middleware

import (
	"net/http"

	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

func RequireLogin(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		sess, err := session.Get("session", c)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Could not get session")
		}

		// Check if the user is authenticated
		if sess.Values["authenticated"] == nil || sess.Values["authenticated"] == false {
			return c.Redirect(http.StatusSeeOther, "/admin/login")
		}

		return next(c)
	}
}
