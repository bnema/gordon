package queries

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/db"
	"github.com/google/uuid"
)

func generateUUID() string {
	return uuid.New().String()
}

// UserInfo holds the essential information for a Github user.
type GithubUserInfo struct {
	Login      string `json:"login"`
	AvatarURL  string `json:"avatar_url"`
	ProfileURL string `json:"html_url"`
	Emails     []string
}

// CreateUser creates a new user along with the associated account, provider, and session.
func CreateUser(a *app.App, accessToken string, browserInfo string, userInfo *GithubUserInfo) error {
	// Check if a user already exists. If so, return an error.
	if exists, err := CheckDBUserExists(a); err != nil || exists {
		return fmt.Errorf("error checking user or user already exists: %v", err)
	}

	user, err := createDBUser(a, userInfo)
	if err != nil {
		return err
	}
	a.DBTables.User.ID = user.ID

	account, err := createDBAccount(a)
	if err != nil {
		return err
	}
	a.DBTables.Account.ID = account.ID

	err = createDBGitHubProvider(a, accessToken, userInfo)
	if err != nil {
		return err
	}

	return createDBSession(a, browserInfo)
}

func createDBUser(a *app.App, userInfo *GithubUserInfo) (*db.User, error) {
	user := &db.User{
		ID:    generateUUID(),
		Name:  userInfo.Login,
		Email: userInfo.Emails[0],
	}

	_, err := a.DB.Exec(
		"INSERT INTO user (id, name, email) VALUES (?, ?, ?)",
		user.ID, user.Name, user.Email,
	)
	return user, err
}

func createDBAccount(a *app.App) (*db.Account, error) {
	account := &db.Account{
		ID: generateUUID(),
	}

	_, err := a.DB.Exec(
		"INSERT INTO account (id, user_id) VALUES (?, ?)",
		account.ID, a.DBTables.User.ID,
	)
	return account, err
}

func createDBGitHubProvider(a *app.App, accessToken string, userInfo *GithubUserInfo) error {
	provider := &db.Provider{
		ID:          generateUUID(),
		AccessToken: accessToken,
		Email:       userInfo.Emails[0],
		Login:       userInfo.Login,
		AvatarURL:   userInfo.AvatarURL,
		ProfileURL:  userInfo.ProfileURL,
	}

	query := "INSERT INTO provider (id, account_id, name, access_token, login, avatar_url, profile_url, email) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
	_, err := a.DB.Exec(query, provider.ID, a.DBTables.Account.ID, "GitHub", provider.AccessToken, provider.Login, provider.AvatarURL, provider.ProfileURL, provider.Email)
	return err
}

// createSession creates a session for the user.
func createDBSession(a *app.App, browserInfo string) error {
	sessions := &db.Sessions{
		ID: generateUUID(),
	}

	query := "INSERT INTO sessions (id, account_id, browser_info, expires, is_online) VALUES (?, ?, ?, ?, ?)"
	_, err := a.DB.Exec(query, sessions.ID, a.DBTables.Account.ID, browserInfo, time.Now().Add(time.Hour*24).Format(time.RFC3339), true)
	a.DBTables.Sessions.ID = sessions.ID
	return err
}

func CreateOrUpdateDBSession(a *app.App, accessToken string, browserInfo string) error {
	existingSessionID := a.DBTables.Sessions.ID

	queryFindExistingSession := "SELECT id FROM sessions WHERE account_id = ? AND browser_info = ?"
	err := a.DB.QueryRow(queryFindExistingSession, a.DBTables.Account.ID, browserInfo).Scan(&existingSessionID)
	if err != nil {
		return err
	}

	// Calculate the new expiry time
	newExpiryTime := time.Now().Add(time.Hour * 24).Format(time.RFC3339)

	if existingSessionID != "" {
		// Update the session if it exists
		_, err = a.DB.Exec("UPDATE sessions SET expires = ?, is_online = ? WHERE id = ?", newExpiryTime, true, existingSessionID)
		if err != nil {
			return err
		}
	} else {
		// Create a new session if it doesn't exist
		queryInsertSession := "INSERT INTO sessions (account_id, browser_info, expires, is_online) VALUES (?, ?, ?, ?, ?)"
		_, err = a.DB.Exec(queryInsertSession, a.DBTables.Account.ID, browserInfo, time.Now().Add(time.Hour*24).Format(time.RFC3339), true)
		if err != nil {
			return err
		}
	}

	return nil
}

func CheckDBAccountAccessToken(a *app.App, accessToken string) (bool, error) {
	var existingAccessToken string
	err := a.DB.QueryRow("SELECT access_token FROM provider WHERE name = ?", "GitHub").Scan(&existingAccessToken)
	if err != nil {
		return false, err
	}

	if existingAccessToken == accessToken {
		return true, nil
	}

	return false, nil
}

func CheckDBUserExists(a *app.App) (bool, error) {
	var userID string
	err := a.DB.QueryRow("SELECT id FROM user").Scan(&userID)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}

	if userID != "" {
		return true, nil
	}

	return false, nil
}
