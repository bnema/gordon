package handler

import (
	"log"
	"net/http"
	"time"

	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

// ResetSession resets the session cookie and session data
func ResetSession(c echo.Context) error {
	// Manually expire the cookie
	cookie := new(http.Cookie)
	cookie.Name = "session"
	cookie.Value = ""
	cookie.Path = "/"
	cookie.Expires = time.Unix(0, 0)
	cookie.MaxAge = -1 // Clear immediately
	c.SetCookie(cookie)
	// Invalidate the existing session
	sess, err := session.Get("session", c)
	if err != nil {
		log.Println(err)
		return echo.NewHTTPError(http.StatusInternalServerError, "Could not get session")
	}
	sess.Options.MaxAge = -1
	if err := sess.Save(c.Request(), c.Response()); err != nil {
		log.Println("Could not delete session:", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "Could not delete session")
	}

	return nil
}
