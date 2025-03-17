package handlers

import (
	"net/http"
	"time"

	"github.com/bnema/gordon/internal/db/queries"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

// ===== Session Types =====

// SessionRequest represents the request body for session creation
type SessionRequest struct {
	Token       string `json:"token"`
	UserID      string `json:"userId"`
	BrowserInfo string `json:"browserInfo"`
}

// SessionResponse represents the response for session operations
type SessionResponse struct {
	SessionID string `json:"sessionID"`
	AccountID string `json:"accountID"`
	Username  string `json:"username"`
	Expires   string `json:"expires"`
	Success   bool   `json:"success"`
}

// InvalidateSessionRequest represents the request body for invalidating a session
type InvalidateSessionRequest struct {
	SessionID string `json:"sessionId"`
}

// ===== Login Types =====

// LoginRequest represents the request body for login
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginResponse represents the response for login
type LoginResponse struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Success  bool   `json:"success"`
}

// ===== Login Handler =====

// LoginAPI handles the login API endpoint for the SvelteKit admin UI
func LoginAPI(c echo.Context, a *server.App) error {
	// Parse request body
	var req LoginRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "Invalid request body",
		})
	}

	// For now, we'll use the token-based authentication
	// In the future, this could be replaced with proper username/password authentication
	configToken, err := a.Config.GetToken()
	if err != nil {
		logger.Error("Error retrieving token from config", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"success": false,
			"error":   "Server error",
		})
	}

	// Check if the password matches the token
	if req.Password != configToken {
		logger.Warn("Invalid login attempt", "username", req.Username)
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"success": false,
			"error":   "Invalid credentials",
		})
	}

	// Check if any user exists in the database
	userExists, err := queries.CheckDBUserExists(a.DB)
	if err != nil {
		logger.Error("Error checking if user exists", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"success": false,
			"error":   "Server error",
		})
	}

	// If no user exists, create a new one with the provided username
	var accountID string
	if !userExists {
		// Create a user with the provided username
		user, accID, err := queries.CreateSimpleUser(a.DB, req.Username)
		if err != nil {
			logger.Error("Error creating user", "error", err)
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"success": false,
				"error":   "Server error",
			})
		}
		accountID = accID
		logger.Info("Created new user", "username", user.Name, "account_id", accountID)
	} else {
		// Get the first user's account ID
		accountID, err = queries.GetFirstUserAccountID(a.DB)
		if err != nil {
			logger.Error("Error getting first user account ID", "error", err)
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"success": false,
				"error":   "Server error",
			})
		}
	}

	// Get the session from the request
	sess, err := session.Get("session", c)
	if err != nil {
		logger.Error("Failed to get session", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"success": false,
			"error":   "Failed to get session",
		})
	}

	// Generate a session token
	sessionToken := queries.GenerateUUID()
	browserInfo := c.Request().UserAgent()

	// Create a session in the database
	dbSession, err := queries.CreateDBSession(a.DB, browserInfo, sessionToken, accountID)
	if err != nil {
		logger.Error("Failed to create session in database", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"success": false,
			"error":   "Failed to create session",
		})
	}

	// Set session values
	sess.Values["authenticated"] = true
	sess.Values["accountID"] = accountID
	sess.Values["expires"] = dbSession.Expires
	sess.Values["sessionID"] = dbSession.ID
	sess.Values["isOnline"] = dbSession.IsOnline

	// Save the session
	if err := sess.Save(c.Request(), c.Response()); err != nil {
		logger.Error("Failed to save session", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"success": false,
			"error":   "Failed to save session",
		})
	}

	// Return user information
	return c.JSON(http.StatusOK, LoginResponse{
		ID:       accountID,
		Username: req.Username,
		Success:  true,
	})
}

// ===== Session Handlers =====

// CreateSessionAPI handles the creation of a new session
func CreateSessionAPI(c echo.Context, a *server.App) error {
	// Parse request body
	var req SessionRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "Invalid request body",
		})
	}

	// Get the session from the request
	sess, err := session.Get("session", c)
	if err != nil {
		logger.Warn("Failed to get session", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"success": false,
			"error":   "Failed to get session",
		})
	}

	// Set session values
	sess.Values["accountID"] = req.UserID
	sess.Values["sessionID"] = req.Token

	// Save the session
	if err := sess.Save(c.Request(), c.Response()); err != nil {
		logger.Warn("Failed to save session", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"success": false,
			"error":   "Failed to save session",
		})
	}

	// Set session expiration time (30 minutes)
	expiresAt := time.Now().Add(30 * time.Minute)

	// Store session in database
	_, err = queries.CreateDBSession(a.DB, req.BrowserInfo, req.Token, req.UserID)
	if err != nil {
		logger.Warn("Failed to store session in database", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"success": false,
			"error":   "Failed to store session in database",
		})
	}

	// Get user information
	username, err := queries.GetUsernameByID(a.DB, req.UserID)
	if err != nil {
		logger.Warn("Failed to get username", "error", err)
		// Continue anyway, as the session is already created
	}

	// Return session information
	return c.JSON(http.StatusOK, SessionResponse{
		SessionID: req.Token,
		AccountID: req.UserID,
		Username:  username,
		Expires:   expiresAt.Format(time.RFC3339),
		Success:   true,
	})
}

// ValidateSessionAPI validates an existing session
func ValidateSessionAPI(c echo.Context, a *server.App) error {
	// This endpoint is already protected by RequireLogin middleware,
	// so if we reach here, the session is valid

	// Get the session from the request
	sess, err := session.Get("session", c)
	if err != nil {
		logger.Warn("Failed to get session", "error", err)
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"success": false,
			"error":   "Invalid session",
		})
	}

	// Get session values
	accountID, ok := sess.Values["accountID"].(string)
	if !ok {
		logger.Warn("Account ID not found or invalid type")
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"success": false,
			"error":   "Invalid session",
		})
	}

	sessionID, ok := sess.Values["sessionID"].(string)
	if !ok {
		logger.Warn("Session ID not found or invalid type")
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"success": false,
			"error":   "Invalid session",
		})
	}

	// Get session expiration time
	expirationTime, err := queries.GetSessionExpiration(a.DB, accountID, sessionID, time.Now())
	if err != nil {
		logger.Warn("Failed to get session expiration time", "error", err)
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"success": false,
			"error":   "Invalid session",
		})
	}

	// Get username
	username, err := queries.GetUsernameByID(a.DB, accountID)
	if err != nil {
		logger.Warn("Failed to get username", "error", err)
		// Continue anyway, as the session is valid
	}

	// Return session information
	return c.JSON(http.StatusOK, SessionResponse{
		SessionID: sessionID,
		AccountID: accountID,
		Username:  username,
		Expires:   expirationTime.Format(time.RFC3339),
		Success:   true,
	})
}

// InvalidateSessionAPI invalidates an existing session
func InvalidateSessionAPI(c echo.Context, a *server.App) error {
	// Get the session from the request
	sess, err := session.Get("session", c)
	if err != nil {
		logger.Warn("Failed to get session", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"success": false,
			"error":   "Failed to get session",
		})
	}

	// Get session values
	accountID, ok := sess.Values["accountID"].(string)
	if !ok {
		logger.Warn("Account ID not found or invalid type")
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "Invalid session",
		})
	}

	sessionID, ok := sess.Values["sessionID"].(string)
	if !ok {
		logger.Warn("Session ID not found or invalid type")
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "Invalid session",
		})
	}

	// Delete session from database
	err = queries.DeleteDBSession(a.DB, accountID, sessionID)
	if err != nil {
		logger.Warn("Failed to delete session from database", "error", err)
		// Continue anyway to clear the cookie
	}

	// Clear session values
	sess.Values = make(map[interface{}]interface{})
	sess.Options.MaxAge = -1 // Delete the cookie

	// Save the session (which will delete the cookie)
	if err := sess.Save(c.Request(), c.Response()); err != nil {
		logger.Warn("Failed to save session", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"success": false,
			"error":   "Failed to save session",
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"success": true,
	})
}
