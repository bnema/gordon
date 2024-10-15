package middleware

import (
	"net/http"
	"time"

	"github.com/bnema/gordon/internal/httpserve/handlers"
	"github.com/bnema/gordon/internal/server"

	"github.com/labstack/echo/v4"
)

func redirectToLogin(c echo.Context, a *server.App) error {
	return c.Redirect(http.StatusSeeOther, a.Config.Admin.Path+"/login")
}

func RequireLogin(a *server.App) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			const maxRetries = 3
			var err error
			for i := 0; i < maxRetries; i++ {
				err = handlers.ValidateSessionAndUser(c, a)
				if err == nil {
					return next(c)
				}
				if i < maxRetries-1 {
					time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
				}
			}
			// propose the redirect auth url as httpcode
			return c.Redirect(http.StatusSeeOther, a.Config.Admin.Path+"/login")
		}

	}
}

// func DeleteCookieAndRedirectToLogin(c echo.Context, a *server.App) error {
// 	// Delete session cookie
// 	cookie := new(http.Cookie)
// 	cookie.Name = "session"
// 	cookie.Value = ""
// 	cookie.Expires = time.Now().Add(-1 * time.Hour)
// 	c.SetCookie(cookie)

// 	return redirectToLogin(c, a)
// }
