package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/bnema/gordon/internal/db/queries"
	"github.com/bnema/gordon/internal/server"
	"github.com/gorilla/sessions"
	"github.com/labstack/echo/v4"
)

func Logout(c echo.Context, a *server.App) error {
	// Retrieve the session
	sess, err := getSession(c)
	if err != nil || sess == nil {
		// if there is no session, redirect to the login page
		return c.Redirect(http.StatusSeeOther, a.Config.Admin.Path+"/login")

	}

	// Invalidate the session in the database
	if sess != nil {
		err := queries.InvalidateDBSession(a, sess.Values["sessionID"].(string))
		if err != nil {
			return fmt.Errorf("failed to invalidate session: %w", err)
		}
	}

	// Delete the session
	sess.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	}

	err = sess.Save(c.Request(), c.Response())
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}

	// Drop the cookie
	cookie := new(http.Cookie)
	cookie.Name = "session"
	cookie.Value = ""
	cookie.Expires = time.Now().Add(-1 * time.Hour)
	c.SetCookie(cookie)

	return c.Redirect(http.StatusSeeOther, a.Config.Admin.Path+"/login")
}
