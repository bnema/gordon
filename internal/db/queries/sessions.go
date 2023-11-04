package queries

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/db"
	"github.com/bnema/gordon/internal/server"
)

// createSession creates a session for the user.
func createDBSession(a *server.App, browserInfo string, accessToken string, accountID string) error {
	expireTime := time.Now().Add(time.Hour * 24).Format(time.RFC3339)
	sessions := &db.Sessions{
		ID:          generateUUID(),
		AccessToken: accessToken,
	}

	query := "INSERT INTO sessions (id, account_id, access_token, browser_info, expires, is_online) VALUES (?, ?, ?, ?, ?, ?)"
	_, err := a.DB.Exec(query, sessions.ID, accountID, accessToken, browserInfo, expireTime, true)
	if err != nil {
		return err
	}

	// Update global state here if really necessary
	a.DBTables.Sessions.ID = sessions.ID
	a.DBTables.Sessions.AccessToken = accessToken
	a.DBTables.Sessions.Expires = expireTime

	return nil
}

// GetDBUserSession gets the user session from the database based on the access token and browser info.
func GetDBUserSession(a *server.App, accessToken string, browserInfo string) (*db.Sessions, error) {
	query := "SELECT sessions.id, sessions.account_id, sessions.access_token, sessions.browser_info, sessions.expires, sessions.is_online FROM sessions INNER JOIN account ON sessions.account_id = account.id INNER JOIN provider ON account.id = provider.account_id WHERE sessions.access_token = ? AND sessions.browser_info = ?"
	err := a.DB.QueryRow(query, accessToken, browserInfo).Scan(&a.DBTables.Sessions.ID, &a.DBTables.Sessions.AccountID, &a.DBTables.Sessions.AccessToken, &a.DBTables.Sessions.BrowserInfo, &a.DBTables.Sessions.Expires, &a.DBTables.Sessions.IsOnline)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("error getting session: %w", err)
	}

	return &a.DBTables.Sessions, nil
}

func updateDBSession(a *server.App, accessToken string, browserInfo string) error {
	expireTime := time.Now().Add(time.Hour * 24).Format(time.RFC3339)
	query := "UPDATE sessions SET access_token = ?, expires = ? WHERE id = ?"
	_, err := a.DB.Exec(query, accessToken, expireTime, a.DBTables.Sessions.ID)
	if err != nil {
		return err
	}

	return nil
}

func CreateOrUpdateDBSession(a *server.App, accessToken string, browserInfo string) error {
	// Check if the session exists using the access token and browser info
	session, err := GetDBUserSession(a, accessToken, browserInfo)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("error checking for session: %w", err)
	}

	// If the session exists, update the session
	if session != nil {
		err := updateDBSession(a, accessToken, browserInfo)
		if err != nil {
			return fmt.Errorf("error updating session: %w", err)
		}
	}

	// If the session does not exist, create the session
	if session == nil {
		err := createDBSession(a, browserInfo, accessToken, a.DBTables.Account.ID)
		if err != nil {
			return fmt.Errorf("error creating session: %w", err)
		}
	}

	return nil
}

// DeleteSession deletes the session from the database.
func DeleteDBSession(a *server.App, accountID string, sessionID string) error {
	query := "DELETE FROM sessions WHERE account_id = ? AND id = ?"
	_, err := a.DB.Exec(query, accountID, sessionID)
	if err != nil {
		return err
	}

	return nil
}

func GetSessionExpiration(a *server.App, accountID string, sessionID string, currentTime time.Time) (time.Time, error) {
	var expirationTime string
	// with userID and sessionID we can get the expiration time
	query := "SELECT expires FROM sessions WHERE account_id = ? AND id = ?"
	err := a.DB.QueryRow(query, accountID, sessionID).Scan(&expirationTime)
	if err != nil {
		return currentTime, err
	}

	// convert the expiration time to a time.Time
	expirationTimeParsed, err := time.Parse(time.RFC3339, expirationTime)
	if err != nil {
		return currentTime, err
	}

	return expirationTimeParsed, nil
}

func ExtendSessionExpiration(a *server.App, accountID string, sessionID string, newExpirationTime time.Time) error {
	query := "UPDATE sessions SET expires = ? WHERE account_id = ? AND id = ?"
	_, err := a.DB.Exec(query, newExpirationTime.Format(time.RFC3339), accountID, sessionID)
	if err != nil {
		return err
	}

	return nil
}

func CheckDBSessionExists(a *server.App, sessionID string) (bool, error) {
	var sessionExists bool
	query := "SELECT EXISTS(SELECT 1 FROM sessions WHERE id = ?)"
	err := a.DB.QueryRow(query, sessionID).Scan(&sessionExists)
	if err != nil {
		return false, err
	}

	return sessionExists, nil
}

func InvalidateDBSession(a *server.App, sessionID string) error {
	query := "UPDATE sessions SET is_online = ? WHERE id = ?"
	_, err := a.DB.Exec(query, false, sessionID)
	if err != nil {
		return err
	}

	return nil
}
