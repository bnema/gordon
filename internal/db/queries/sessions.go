package queries

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/db"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/google/uuid"
)

// SessionQueries contains all SQL queries for session operations
type SessionQueries struct {
	InsertSession       string
	GetSessionByToken   string
	UpdateSessionToken  string
	DeleteSession       string
	GetSessionExpiry    string
	UpdateSessionExpiry string
	CheckSessionExists  string
	InvalidateSession   string
}

// NewSessionQueries returns a new instance of SessionQueries
func NewSessionQueries() *SessionQueries {
	return &SessionQueries{
		InsertSession:       "INSERT INTO sessions (id, account_id, access_token, browser_info, expires, is_online) VALUES (?, ?, ?, ?, ?, ?)",
		GetSessionByToken:   "SELECT sessions.id, sessions.account_id, sessions.access_token, sessions.browser_info, sessions.expires, sessions.is_online FROM sessions INNER JOIN account ON sessions.account_id = account.id INNER JOIN provider ON account.id = provider.account_id WHERE sessions.access_token = ? AND sessions.browser_info = ?",
		UpdateSessionToken:  "UPDATE sessions SET access_token = ?, expires = ? WHERE id = ?",
		DeleteSession:       "DELETE FROM sessions WHERE account_id = ? AND id = ?",
		GetSessionExpiry:    "SELECT expires FROM sessions WHERE account_id = ? AND id = ?",
		UpdateSessionExpiry: "UPDATE sessions SET expires = ? WHERE account_id = ? AND id = ?",
		CheckSessionExists:  "SELECT EXISTS(SELECT 1 FROM sessions WHERE id = ?)",
		InvalidateSession:   "UPDATE sessions SET is_online = ? WHERE id = ?",
	}
}

// CreateDBSession creates a session for the user.
func CreateDBSession(database *sql.DB, browserInfo string, accessToken string, accountID string) (*db.Sessions, error) {
	expireTime := time.Now().Add(time.Hour * 24).Format(time.RFC3339)
	sessionID := uuid.NewString()

	_, err := database.Exec(
		NewSessionQueries().InsertSession,
		sessionID, accountID, accessToken, browserInfo, expireTime, true,
	)

	if err != nil {
		logger.Error("Failed to create session in database", "error", err, "session_id", sessionID, "account_id", accountID)
		return nil, err
	}

	// Create a Sessions struct to return
	session := &db.Sessions{
		ID:          sessionID,
		AccountID:   accountID,
		AccessToken: accessToken,
		BrowserInfo: browserInfo,
		Expires:     expireTime,
		IsOnline:    true,
	}

	logger.Debug("Session created in database", "session_id", sessionID, "account_id", accountID, "expires", expireTime)
	return session, nil
}

// GetDBUserSession gets the user session from the database based on the access token and browser info.
func GetDBUserSession(database *sql.DB, accessToken string, browserInfo string) (*db.Sessions, error) {
	session := &db.Sessions{}

	err := database.QueryRow(
		NewSessionQueries().GetSessionByToken,
		accessToken,
		browserInfo,
	).Scan(
		&session.ID,
		&session.AccountID,
		&session.AccessToken,
		&session.BrowserInfo,
		&session.Expires,
		&session.IsOnline,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		logger.Warn("Failed to get session", "error", err)
		return nil, fmt.Errorf("error getting session: %w", err)
	}

	logger.Debug("Session retrieved from database", "session", session)

	return session, nil
}

// UpdateDBSession updates an existing session with a new access token
func UpdateDBSession(database *sql.DB, sessionID string, accessToken string) error {
	expireTime := time.Now().Add(time.Hour * 24).Format(time.RFC3339)

	_, err := database.Exec(
		NewSessionQueries().UpdateSessionToken,
		accessToken,
		expireTime,
		sessionID,
	)

	if err != nil {
		logger.Error("Failed to update session in database", "error", err, "session_id", sessionID)
		return err
	}

	logger.Debug("Session updated in database", "sessionID", sessionID, "expires", expireTime)
	return nil
}

// CreateOrUpdateSession creates a new session or updates an existing one
func CreateOrUpdateSession(database *sql.DB, accountID string, accessToken string, browserInfo string) (*db.Sessions, error) {
	// Check if a session exists for this account with the same browser info
	// (Token might change, so check by account/browser)
	existingSession, err := GetSessionByAccountAndBrowser(database, accountID, browserInfo)
	if err != nil && err != sql.ErrNoRows {
		logger.Error("Failed to check for existing session", "error", err, "accountID", accountID)
		return nil, fmt.Errorf("error checking for existing session: %w", err)
	}

	expireTime := time.Now().Add(time.Hour * 24).Format(time.RFC3339)

	// If the session exists, update its token and expiry
	if existingSession != nil {
		logger.Debug("Existing session found, updating token and expiry", "sessionID", existingSession.ID, "accountID", accountID)
		_, err := database.Exec(
			NewSessionQueries().UpdateSessionToken,
			accessToken,
			expireTime,
			existingSession.ID,
		)
		if err != nil {
			logger.Error("Failed to update existing session", "error", err, "session_id", existingSession.ID)
			return nil, fmt.Errorf("error updating session: %w", err)
		}
		// Update the struct we have in memory
		existingSession.AccessToken = accessToken
		existingSession.Expires = expireTime
		existingSession.IsOnline = true // Ensure it's marked online
		logger.Info("Session updated successfully", "sessionID", existingSession.ID)
		return existingSession, nil
	} else {
		// If the session does not exist, create a new one
		logger.Debug("No existing session found, creating new session", "accountID", accountID)
		sessionID := uuid.NewString() // Use uuid directly
		_, err := database.Exec(
			NewSessionQueries().InsertSession,
			sessionID, accountID, accessToken, browserInfo, expireTime, true,
		)
		if err != nil {
			logger.Error("Failed to create new session", "error", err, "accountID", accountID)
			return nil, fmt.Errorf("error creating session: %w", err)
		}
		// Create the session struct to return
		newSession := &db.Sessions{
			ID:          sessionID,
			AccountID:   accountID,
			AccessToken: accessToken,
			BrowserInfo: browserInfo,
			Expires:     expireTime,
			IsOnline:    true,
		}
		logger.Info("New session created successfully", "sessionID", newSession.ID)
		return newSession, nil
	}
}

// GetSessionByAccountAndBrowser finds a session by account ID and browser info
func GetSessionByAccountAndBrowser(database *sql.DB, accountID string, browserInfo string) (*db.Sessions, error) {
	session := &db.Sessions{}
	// Define the query inline or add to SessionQueries struct
	query := "SELECT id, account_id, access_token, browser_info, expires, is_online FROM sessions WHERE account_id = ? AND browser_info = ?"
	err := database.QueryRow(query, accountID, browserInfo).Scan(
		&session.ID,
		&session.AccountID,
		&session.AccessToken,
		&session.BrowserInfo,
		&session.Expires,
		&session.IsOnline,
	)
	if err != nil {
		// Return sql.ErrNoRows if not found
		return nil, err
	}
	return session, nil
}

// DeleteDBSession deletes the session from the database.
func DeleteDBSession(database *sql.DB, accountID string, sessionID string) error {
	_, err := database.Exec(
		NewSessionQueries().DeleteSession,
		accountID,
		sessionID,
	)

	if err != nil {
		logger.Error("Failed to delete session from database", "error", err, "account_id", accountID, "session_id", sessionID)
		return err
	}

	logger.Debug("Session deleted from database", "accountID", accountID, "sessionID", sessionID)
	return nil
}

// GetSessionExpiration gets the expiration time for a session
func GetSessionExpiration(database *sql.DB, accountID string, sessionID string, currentTime time.Time) (time.Time, error) {
	var expirationTime string

	err := database.QueryRow(
		NewSessionQueries().GetSessionExpiry,
		accountID,
		sessionID,
	).Scan(&expirationTime)

	if err != nil {
		return currentTime, err
	}

	// convert the expiration time to a time.Time
	expirationTimeParsed, err := time.Parse(time.RFC3339, expirationTime)
	if err != nil {
		return currentTime, err
	}

	logger.Debug("Session expiration time retrieved", "accountID", accountID, "sessionID", sessionID, "expirationTime", expirationTimeParsed)

	return expirationTimeParsed, nil
}

// ExtendSessionExpiration extends the expiration time for a session
func ExtendSessionExpiration(database *sql.DB, accountID string, sessionID string, newExpirationTime time.Time) error {
	_, err := database.Exec(
		NewSessionQueries().UpdateSessionExpiry,
		newExpirationTime.Format(time.RFC3339),
		accountID,
		sessionID,
	)

	if err != nil {
		logger.Error("Failed to extend session expiration", "error", err, "account_id", accountID, "session_id", sessionID)
		return err
	}

	logger.Debug("Session expiration time extended", "accountID", accountID, "sessionID", sessionID, "newExpirationTime", newExpirationTime)
	return nil
}

// CheckDBSessionExists checks if a session exists
func CheckDBSessionExists(database *sql.DB, sessionID string) (bool, error) {
	var sessionExists bool
	err := database.QueryRow(
		NewSessionQueries().CheckSessionExists,
		sessionID,
	).Scan(&sessionExists)

	if err != nil {
		return false, err
	}

	logger.Debug("Session existence checked", "sessionID", sessionID, "exists", sessionExists)
	return sessionExists, nil
}

// InvalidateDBSession marks a session as offline
func InvalidateDBSession(database *sql.DB, sessionID string) error {
	_, err := database.Exec(
		NewSessionQueries().InvalidateSession,
		false,
		sessionID,
	)

	if err != nil {
		logger.Error("Failed to invalidate session", "error", err, "session_id", sessionID)
		return err
	}

	logger.Debug("Session invalidated", "sessionID", sessionID)
	return nil
}
