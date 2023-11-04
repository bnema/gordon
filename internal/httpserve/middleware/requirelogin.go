package middleware

import (
	"time"

	"github.com/bnema/gordon/internal/httpserve/handler"
	"github.com/bnema/gordon/internal/server"

	"github.com/labstack/echo/v4"
)

func RequireLogin(a *server.App) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			const maxRetries = 3
			var err error
			for i := 0; i < maxRetries; i++ {
				err = handler.ValidateSessionAndUser(c, a)
				if err == nil {
					return next(c)
				}
				if i < maxRetries-1 {
					time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
				}
			}
			return c.JSON(401, map[string]string{"error": err.Error()})
		}
	}
}

// func redirectToLogin(c echo.Context, a *server.App) error {
// 	return c.Redirect(http.StatusSeeOther, a.Config.Admin.Path+"/login")
// }

// func DeleteCookieAndRedirectToLogin(c echo.Context, a *server.App) error {
// 	// Delete session cookie
// 	cookie := new(http.Cookie)
// 	cookie.Name = "session"
// 	cookie.Value = ""
// 	cookie.Expires = time.Now().Add(-1 * time.Hour)
// 	c.SetCookie(cookie)

// 	return redirectToLogin(c, a)
// }
