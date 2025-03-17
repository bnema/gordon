package queries

import (
	"database/sql"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/db"
	"github.com/bnema/gordon/pkg/logger"
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
	sessionID := GenerateUUID()
	
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

// CreateOrUpdateDBSession creates a new session or updates an existing one
func CreateOrUpdateDBSession(database *sql.DB, accessToken string, browserInfo string, accountID string) error {
	// Check if the session exists using the access token and browser info
	session, err := GetDBUserSession(database, accessToken, browserInfo)
	if err != nil && err != sql.ErrNoRows {
		logger.Warn("Failed to check for session", "error", err)
		return fmt.Errorf("error checking for session: %w", err)
	}

	// If the session exists, update the session
	if session != nil {
		err := UpdateDBSession(database, session.ID, accessToken)
		if err != nil {
			logger.Warn("Failed to update session", "error", err)
			return fmt.Errorf("error updating session: %w", err)
		}
		logger.Debug("Session updated in database", "accountID", accountID, "sessionID", session.ID)
	} else {
		// If the session does not exist, create the session
		_, err := CreateDBSession(database, browserInfo, accessToken, accountID)
		if err != nil {
			logger.Warn("Failed to create session", "error", err)
			return fmt.Errorf("error creating session: %w", err)
		}
		logger.Debug("New session created in database", "accountID", accountID)
	}

	return nil
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

// CreateSimpleUser creates a simple user with just a username
func CreateSimpleUser(database *sql.DB, username string) (*db.User, string, error) {
	// Generate UUIDs for user and account
	userID := GenerateUUID()
	accountID := GenerateUUID()
	
	// Begin a transaction
	tx, err := database.Begin()
	if err != nil {
		logger.Error("Failed to begin transaction", "error", err)
		return nil, "", err
	}
	
	// Create the user
	_, err = tx.Exec(
		"INSERT INTO user (id, name) VALUES (?, ?)",
		userID, username,
	)
	if err != nil {
		tx.Rollback()
		logger.Error("Failed to create user", "error", err)
		return nil, "", err
	}
	
	// Create the account
	_, err = tx.Exec(
		"INSERT INTO account (id, user_id) VALUES (?, ?)",
		accountID, userID,
	)
	if err != nil {
		tx.Rollback()
		logger.Error("Failed to create account", "error", err)
		return nil, "", err
	}
	
	// Commit the transaction
	if err := tx.Commit(); err != nil {
		logger.Error("Failed to commit transaction", "error", err)
		return nil, "", err
	}
	
	// Create a User struct to return
	user := &db.User{
		ID:   userID,
		Name: username,
	}
	
	logger.Debug("Simple user created in database", "user_id", userID, "account_id", accountID)
	return user, accountID, nil
}

// GetFirstUserAccountID gets the account ID of the first user in the database
func GetFirstUserAccountID(database *sql.DB) (string, error) {
	var accountID string
	
	err := database.QueryRow(
		"SELECT account.id FROM account INNER JOIN user ON account.user_id = user.id LIMIT 1",
	).Scan(&accountID)
	
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("no users found")
		}
		logger.Error("Failed to get first user account ID", "error", err)
		return "", err
	}
	
	return accountID, nil
}

// GetUserByAccountID gets a user by their account ID
func GetUserByAccountID(database *sql.DB, accountID string) (*db.User, error) {
	var user db.User
	
	err := database.QueryRow(
		"SELECT user.id, user.name, user.email FROM user INNER JOIN account ON user.id = account.user_id WHERE account.id = ?",
		accountID,
	).Scan(&user.ID, &user.Name, &user.Email)
	
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no user found with account ID: %s", accountID)
		}
		logger.Error("Failed to get user by account ID", "error", err, "account_id", accountID)
		return nil, err
	}
	
	return &user, nil
}

// GetSessionByID gets a session by its ID
func GetSessionByID(database *sql.DB, sessionID string) (*db.Sessions, error) {
	var session db.Sessions
	
	err := database.QueryRow(
		"SELECT id, account_id, browser_info, access_token, expires, is_online FROM sessions WHERE id = ?",
		sessionID,
	).Scan(&session.ID, &session.AccountID, &session.BrowserInfo, &session.AccessToken, &session.Expires, &session.IsOnline)
	
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no session found with ID: %s", sessionID)
		}
		logger.Error("Failed to get session by ID", "error", err, "session_id", sessionID)
		return nil, err
	}
	
	return &session, nil
}

// GenerateUUID generates a UUID string
func GenerateUUID() string {
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		randomHex(8),
		randomHex(4),
		randomHex(4),
		randomHex(4),
		randomHex(12),
	)
}

// randomHex generates a random hex string of the specified length
func randomHex(length int) string {
	bytes := make([]byte, length/2)
	if _, err := rand.Read(bytes); err != nil {
		logger.Error("Failed to generate random bytes", "error", err)
		return strings.Repeat("0", length)
	}
	return fmt.Sprintf("%x", bytes)
}
