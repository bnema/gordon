package queries

import (
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/db"
	"github.com/google/uuid"
)

func generateUUID() string {
	return uuid.New().String()
}

// CreateUser creates a new user along with the associated account, provider, and session.
func CreateUser(a *app.App, accessToken string, browserInfo string) error {

	// !! Additionnal security check !!
	// Check if a user already exists. If so, return an error.
	userExists, err := UserExists(a)
	if err != nil {
		return err
	}
	if userExists {
		return fmt.Errorf("user already exists")
	}

	user := db.User{}
	account := db.Account{}

	// Step 1: Create a user
	newUUID := generateUUID()
	user.ID = newUUID
	user.Name = "Test User"
	user.Email = "admin@gordon.cul"

	_, err = a.DB.Exec("INSERT INTO user (id ,name, email) VALUES (?, ?, ?)", user.ID, user.Name, user.Email)
	if err != nil {
		return err
	}

	a.DBTables.User.ID = user.ID
	// Step 2: Create an account for the user with the user ID
	account.ID, err = createAccount(a)
	if err != nil {
		return err
	}
	// Step 3: Create a provider for the user
	if err := createProvider(a, accessToken); err != nil {
		return err
	}

	// Step 4: Create a session for the user
	if err := createSession(a, browserInfo); err != nil {
		return err
	}

	return nil
}

func createAccount(a *app.App) (string, error) {
	account := a.DBTables.Account
	// Generate a new UUID for the account
	account.ID = generateUUID()

	// Insert the account into the database
	_, err := a.DB.Exec("INSERT INTO account (id, user_id) VALUES (?, ?)", account.ID, a.DBTables.User.ID)
	if err != nil {
		return "", err
	}
	a.DBTables.Account.ID = account.ID
	return account.ID, nil
}

func createProvider(a *app.App, accessToken string) error {
	provider := a.DBTables.Provider
	provider.ID = generateUUID()

	queryInsertProvider := "INSERT INTO provider (id, account_id, name, access_token, refresh_token, expires) VALUES (?, ?, ?, ?, ?, ?)"
	_, err := a.DB.Exec(queryInsertProvider, provider.ID, a.DBTables.Account.ID, "GitHub", accessToken, "refreshToken", time.Now().Add(time.Hour*24).Format(time.RFC3339))
	return err
}

// createSession creates a session for the user.
func createSession(a *app.App, browserInfo string) error {
	sessions := a.DBTables.Sessions
	sessions.ID = generateUUID()

	queryInsertSession := "INSERT INTO sessions (id, account_id, browser_info, expires, is_online) VALUES (?, ?, ?, ?, ?)"
	_, err := a.DB.Exec(queryInsertSession, sessions.ID, a.DBTables.Account.ID, browserInfo, time.Now().Add(time.Hour*24).Format(time.RFC3339), true)

	a.DBTables.Sessions.ID = sessions.ID
	return err
}

func CreateOrUpdateSession(a *app.App, accessToken string, browserInfo string) error {
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

func UserExists(a *app.App) (bool, error) {
	var count int
	err := a.DB.QueryRow("SELECT COUNT(*) FROM user").Scan(&count)
	if err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	return false, nil
}

func UpdateAccessToken(a *app.App, accessToken string) error {
	_, err := a.DB.Exec("UPDATE provider SET access_token = ? WHERE name = ?", accessToken, "GitHub")
	return err
}
