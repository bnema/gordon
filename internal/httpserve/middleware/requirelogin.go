package middleware

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/db/queries"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

func RequireLogin(a *app.App) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if err := validateSessionAndUser(c, a); err != nil {
				log.Println("Session validation failed:", err)
				return redirectToLogin(c, a)
			}
			return next(c)
		}
	}
}

func validateSessionAndUser(c echo.Context, a *app.App) error {
	sess, err := session.Get("session", c)
	if err != nil {
		return err
	}

	if cookieExpired, err := isCookieExpired(c); err != nil || cookieExpired {
		return err
	}

	accountID, ok := sess.Values["accountID"].(string)
	if !ok {
		return fmt.Errorf("invalid account ID type")
	}

	sessionID, ok := sess.Values["sessionID"].(string)
	if !ok {
		return fmt.Errorf("invalid session ID type")
	}

	if accountCheck, err := isAccountIDInDB(a, accountID); err != nil || !accountCheck {
		return err
	}

	if sessionExpired, err := isSessionExpiredInDB(a, accountID, sessionID); err != nil || sessionExpired {
		return err
	}

	return nil
}

func redirectToLogin(c echo.Context, a *app.App) error {
	return c.Redirect(http.StatusSeeOther, a.Config.Admin.Path+"/login")
}

func isCookieExpired(c echo.Context) (bool, error) {
	cookie, err := c.Cookie("session")
	if err != nil {
		return false, err
	}
	if cookie.Expires.IsZero() {
		return false, nil // cookie has no expiration time; it's a session cookie
	}
	return time.Now().After(cookie.Expires), nil
}

func isSessionExpiredInDB(a *app.App, userID string, sessionID string) (bool, error) {
	currentTime := time.Now()
	expirationTime, err := queries.GetSessionExpiration(a, userID, sessionID, currentTime)
	if err != nil {
		return false, err
	}

	return currentTime.After(expirationTime), nil
}

func isAccountIDInDB(a *app.App, accountID string) (bool, error) {
	accountExists, err := queries.CheckDBAccountExists(a, accountID)
	if err != nil {
		return false, err
	}

	return accountExists, nil
}
