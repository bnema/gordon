package middleware

import (
	"fmt"
	"net/http"
	"time"

	"github.com/bnema/gordon/internal/db/queries"
	"github.com/bnema/gordon/internal/server"

	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

func RequireLogin(a *server.App) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if err := validateSessionAndUser(c, a); err != nil {
				return DeleteCookieAndRedirectToLogin(c, a)
			}
			return next(c)
		}
	}
}

func validateSessionAndUser(c echo.Context, a *server.App) error {
	sessionExpiryTime := 30 * time.Minute

	sess, err := session.Get("session", c)
	if err != nil {
		return err
	}

	accountID, ok := sess.Values["accountID"].(string)
	if !ok {
		return fmt.Errorf("accountID not found or invalid type")
	}

	sessionID, ok := sess.Values["sessionID"].(string)
	if !ok {
		return fmt.Errorf("sessionID not found or invalid type")
	}

	accountExists, err := isAccountIDInDB(a, accountID)
	if err != nil || !accountExists {
		return fmt.Errorf("account does not exist")
	}

	sessionExpired, err := isSessionExpiredInDB(a, accountID, sessionID)
	if err != nil || sessionExpired {
		return fmt.Errorf("session is expired")
	}

	// Extend session expiration conditionally, e.g., if it's close to expiring
	err = queries.ExtendSessionExpiration(a, accountID, sessionID, time.Now().Add(sessionExpiryTime))
	if err != nil {
		return fmt.Errorf("failed to extend session: %v", err)
	}

	return nil
}

func redirectToLogin(c echo.Context, a *server.App) error {
	return c.Redirect(http.StatusSeeOther, a.Config.Admin.Path+"/login")
}

func isSessionExpiredInDB(a *server.App, userID string, sessionID string) (bool, error) {
	currentTime := time.Now()
	expirationTime, err := queries.GetSessionExpiration(a, userID, sessionID, currentTime)
	if err != nil {
		return false, err
	}

	return currentTime.After(expirationTime), nil
}

func isAccountIDInDB(a *server.App, accountID string) (bool, error) {
	accountExists, err := queries.CheckDBAccountExists(a, accountID)
	if err != nil {
		return false, err
	}

	return accountExists, nil
}

func DeleteCookieAndRedirectToLogin(c echo.Context, a *server.App) error {
	// Delete session cookie
	cookie := new(http.Cookie)
	cookie.Name = "session"
	cookie.Value = ""
	cookie.Expires = time.Now().Add(-1 * time.Hour)
	c.SetCookie(cookie)

	return redirectToLogin(c, a)
}
