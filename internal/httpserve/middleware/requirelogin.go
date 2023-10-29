package middleware

import (
	"fmt"
	"log"
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
				log.Println("Session validation failed:", err)
				return redirectToLogin(c, a)
			}
			return next(c)
		}
	}
}

func validateSessionAndUser(c echo.Context, a *server.App) error {
	sess, err := session.Get("session", c)
	if err != nil {
		return err
	}

	// Retrieve accountID and sessionID from session values
	accountID, ok := sess.Values["accountID"].(string)
	if !ok {
		return fmt.Errorf("invalid account ID type")
	}

	sessionID, ok := sess.Values["sessionID"].(string)
	if !ok {
		return fmt.Errorf("invalid session ID type")
	}

	// Retrieve expires from session values and parse it to time.Time
	expiresStr, ok := sess.Values["expires"].(string)
	if !ok {
		return fmt.Errorf("invalid expires type")
	}
	expires, err := time.Parse(time.RFC3339, expiresStr)
	if err != nil {
		return fmt.Errorf("could not parse expires: %v", err)
	}

	if accountCheck, err := isAccountIDInDB(a, accountID); err != nil || !accountCheck {
		fmt.Println("accountCheck:", accountCheck)
		return err
	}

	if sessionExpired, err := isSessionExpiredInDB(a, accountID, sessionID); err != nil || sessionExpired {
		fmt.Println("sessionExpired:", sessionExpired)
		return err
	}

	if cookieExpired, err := isCookieExpired(c); err != nil || cookieExpired {
		fmt.Println("cookieExpired:", cookieExpired)
		return err
	}

	if time.Now().After(expires) {
		fmt.Println("session expired")
		queries.SessionCleaner(a, time.Now().Format(time.RFC3339))
		return fmt.Errorf("session expired")

	}

	return nil
}

func redirectToLogin(c echo.Context, a *server.App) error {
	return c.Redirect(http.StatusSeeOther, a.Config.Admin.Path+"/login")
}

func isCookieExpired(c echo.Context) (bool, error) {
	cookie, err := c.Cookie("session")
	if err != nil {
		return false, err
	}

	currentTime := time.Now()
	expirationTime := cookie.Expires
	return currentTime.After(expirationTime), nil
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

func DeleteAndRedirectToLogin(c echo.Context, a *server.App) error {
	sess, err := session.Get("session", c)
	if err != nil {
		return err
	}

	// Retrieve accountID and sessionID from session values
	accountID, ok := sess.Values["accountID"].(string)
	if !ok {
		return fmt.Errorf("invalid account ID type")
	}

	sessionID, ok := sess.Values["sessionID"].(string)
	if !ok {
		return fmt.Errorf("invalid session ID type")
	}

	// Delete session from DB
	if err := queries.DeleteDBSession(a, accountID, sessionID); err != nil {
		return err
	}

	// Delete session cookie
	cookie := new(http.Cookie)
	cookie.Name = "session"
	cookie.Value = ""
	cookie.Expires = time.Now().Add(-1 * time.Hour)
	c.SetCookie(cookie)

	return redirectToLogin(c, a)
}
