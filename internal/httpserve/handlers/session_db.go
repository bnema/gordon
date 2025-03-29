package handlers

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/db/queries"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

func ValidateSessionAndUser(c echo.Context, a *server.App) error {
	sessionExpiryTime := 30 * time.Minute

	sess, err := session.Get("session", c)
	if err != nil {
		logger.Warn("Failed to get session from cookie store", "error", err)
		return fmt.Errorf("failed to get session: %w", err)
	}

	accountID, ok := sess.Values["accountID"].(string)
	if !ok || accountID == "" {
		logger.Warn("Account ID not found in session or is empty")
		return fmt.Errorf("accountID not found or invalid")
	}

	sessionID, ok := sess.Values["sessionID"].(string)
	if !ok || sessionID == "" {
		logger.Warn("Session ID not found in session or is empty")
		return fmt.Errorf("sessionID not found or invalid")
	}

	authenticated, ok := sess.Values["authenticated"].(bool)
	if !ok || !authenticated {
		logger.Warn("User not authenticated according to session cookie", "ok", ok, "authenticated_val", sess.Values["authenticated"])
		return fmt.Errorf("user not authenticated")
	}

	accountExists, err := IsAccountIDInDB(a, accountID)
	if err != nil {
		logger.Error("Failed to check account existence", "error", err, "accountID", accountID)
		return fmt.Errorf("failed checking account existence: %w", err)
	}
	if !accountExists {
		logger.Warn("Account associated with session not found in DB", "accountID", accountID)
		return fmt.Errorf("account does not exist")
	}

	isValidInDB, err := IsSessionValidInDB(a, sessionID)
	if err != nil {
		logger.Error("Failed to check session validity in DB", "error", err, "sessionID", sessionID)
		return fmt.Errorf("failed checking session validity in DB: %w", err)
	}
	if !isValidInDB {
		logger.Warn("Session is invalid in DB (expired or not online)", "sessionID", sessionID)
		return fmt.Errorf("session invalid or expired")
	}

	err = queries.ExtendSessionExpiration(a.DB, accountID, sessionID, time.Now().Add(sessionExpiryTime))
	if err != nil {
		logger.Error("Failed to extend session in DB", "error", err, "accountID", accountID, "sessionID", sessionID)
		return fmt.Errorf("failed to extend session: %w", err)
	}
	sess.Values["expires"] = time.Now().Add(sessionExpiryTime).Format(time.RFC3339)
	if err := sess.Save(c.Request(), c.Response()); err != nil {
		logger.Error("Failed to save updated expiry to cookie session", "error", err, "sessionID", sessionID)
	}

	logger.Debug("Session validated and extended successfully", "accountID", accountID, "sessionID", sessionID)
	return nil
}

func IsSessionValidInDB(a *server.App, sessionID string) (bool, error) {
	var dbExpiresStr string
	var isOnline bool

	query := "SELECT expires, is_online FROM sessions WHERE id = ?"
	err := a.DB.QueryRow(query, sessionID).Scan(&dbExpiresStr, &isOnline)

	if err != nil {
		if err == sql.ErrNoRows {
			logger.Warn("Session not found in DB", "sessionID", sessionID)
			return false, nil
		}
		logger.Error("Failed to query session validity from DB", "error", err, "sessionID", sessionID)
		return false, err
	}

	if !isOnline {
		logger.Warn("Session found in DB but marked as not online", "sessionID", sessionID)
		return false, nil
	}

	expirationTime, err := time.Parse(time.RFC3339, dbExpiresStr)
	if err != nil {
		logger.Error("Failed to parse session expiration time from DB", "error", err, "sessionID", sessionID, "expires_str", dbExpiresStr)
		return false, fmt.Errorf("invalid expiry format in DB: %w", err)
	}

	if time.Now().After(expirationTime) {
		logger.Warn("Session found in DB but expired", "sessionID", sessionID, "expires_at", expirationTime)
		return false, nil
	}

	return true, nil
}

func IsAccountIDInDB(a *server.App, accountID string) (bool, error) {
	accountExists, err := queries.CheckDBAccountExists(a.DB, accountID)
	if err != nil {
		logger.Error("Failed to check account existence", "error", err, "accountID", accountID)
		return false, err
	}
	logger.Debug("Account existence check", "accountID", accountID, "exists", accountExists)
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
