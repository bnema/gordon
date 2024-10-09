package handlers

import (
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/db/queries"
	"github.com/bnema/gordon/internal/server"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

func ValidateSessionAndUser(c echo.Context, a *server.App) error {
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

	accountExists, err := IsAccountIDInDB(a, accountID)
	if err != nil || !accountExists {
		return fmt.Errorf("account does not exist")
	}

	sessionExpired, err := IsSessionExpiredInDB(a, accountID, sessionID)
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

func IsSessionExpiredInDB(a *server.App, accountID string, sessionID string) (bool, error) {
	currentTime := time.Now()
	expirationTime, err := queries.GetSessionExpiration(a, accountID, sessionID, currentTime)
	if err != nil {
		return false, err
	}

	return currentTime.After(expirationTime), nil
}

func IsAccountIDInDB(a *server.App, accountID string) (bool, error) {
	accountExists, err := queries.CheckDBAccountExists(a, accountID)
	if err != nil {
		return false, err
	}

	return accountExists, nil
}

// ResetSession resets the session cookie and session data
// func ResetSession(c echo.Context) error {
// 	// Manually expire the cookie
// 	cookie := new(http.Cookie)
// 	cookie.Name = "session"
// 	cookie.Value = ""
// 	cookie.Path = "/"
// 	cookie.Expires = time.Unix(0, 0)
// 	cookie.MaxAge = -1 // Clear immediately
// 	c.SetCookie(cookie)
// 	// Invalidate the existing session
// 	sess, err := session.Get("session", c)
// 	if err != nil {
// 		log.Println(err)
// 		return echo.NewHTTPError(http.StatusInternalServerError, "Could not get session")
// 	}
// 	sess.Options.MaxAge = -1
// 	if err := sess.Save(c.Request(), c.Response()); err != nil {
// 		log.Println("Could not delete session:", err)
// 		return echo.NewHTTPError(http.StatusInternalServerError, "Could not delete session")
// 	}

// 	return nil
// }
