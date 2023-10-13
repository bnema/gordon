package queries

import (
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/db"
)

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
	// Step 1: Create an entry in the user table
	// TODO : obtain the user name and email from the GitHub API
	result, err := a.DB.Exec("INSERT INTO user (name, email) VALUES (?, ?)", "admin", "admin@gordon")
	if err != nil {
		return err
	}

	// Get the last insert ID from the result of the INSERT operation
	user.ID, err = result.LastInsertId()
	if err != nil {
		return fmt.Errorf("error while fetching LastInsertId: %w", err)
	}

	fmt.Printf("User ID: %d\n", user.ID)

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

func createAccount(a *app.App) (int64, error) {
	fmt.Println("Whats the value of a.DBTables.User.ID?", a.DBTables.User.ID)
	account := a.DBTables.Account
	queryInsertAccount := "INSERT INTO account (user_id) VALUES (?) RETURNING id"
	// Execute the INSERT operation
	result, err := a.DB.Exec(queryInsertAccount, a.DBTables.User.ID)
	if err != nil {
		return 0, fmt.Errorf("error while inserting into account: %w", err)
	}

	// Get the last inserted ID
	account.ID, err = result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("error while fetching LastInsertId for account: %w", err)
	}

	return account.ID, nil
}

func createProvider(a *app.App, accessToken string) error {
	queryInsertProvider := "INSERT INTO provider (account_id, name, access_token, refresh_token, expires) VALUES (?, ?, ?, ?, ?)"
	_, err := a.DB.Exec(queryInsertProvider, a.DBTables.Account.ID, "GitHub", accessToken, "refreshToken", time.Now().Add(time.Hour*24).Format(time.RFC3339))
	return err
}

// createSession creates a session for the user.
func createSession(a *app.App, browserInfo string) error {
	queryInsertSession := "INSERT INTO sessions (account_id, browser_info, expires, is_online) VALUES (?, ?, ?, ?)"
	_, err := a.DB.Exec(queryInsertSession, a.DBTables.Account.ID, browserInfo, time.Now().Add(time.Hour*24).Format(time.RFC3339), true)
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

	if existingSessionID > 0 {
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
