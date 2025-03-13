package queries

import (
	"database/sql"
	"github.com/bnema/gordon/internal/db"
)

// AccountQueries contains all SQL queries for account operations
type AccountQueries struct {
	InsertAccount     string
	CheckAccountExists string
	GetAccountByID    string
}

// NewAccountQueries returns a new instance of AccountQueries
func NewAccountQueries() *AccountQueries {
	return &AccountQueries{
		InsertAccount:     "INSERT INTO account (id, user_id) VALUES (?, ?)",
		CheckAccountExists: "SELECT EXISTS(SELECT 1 FROM account WHERE id = ?)",
		GetAccountByID:    "SELECT id, user_id FROM account WHERE id = ?",
	}
}

// CreateDBAccount creates a new account in the database
func CreateDBAccount(database *sql.DB, userID string) (*db.Account, error) {
	account := &db.Account{
		ID:     generateUUID(),
		UserID: userID,
	}

	_, err := database.Exec(
		NewAccountQueries().InsertAccount,
		account.ID, userID,
	)
	
	return account, err
}

// CheckDBAccountExists checks if an account exists in the database
func CheckDBAccountExists(database *sql.DB, accountID string) (bool, error) {
	var exists bool
	
	err := database.QueryRow(
		NewAccountQueries().CheckAccountExists,
		accountID,
	).Scan(&exists)
	
	if err != nil {
		return false, err
	}
	
	return exists, nil
}

// GetDBAccountByID gets an account from the database by ID
func GetDBAccountByID(database *sql.DB, accountID string) (*db.Account, error) {
	account := &db.Account{
		ID: accountID,
	}
	
	err := database.QueryRow(
		NewAccountQueries().GetAccountByID,
		accountID,
	).Scan(&account.ID, &account.UserID)
	
	if err != nil {
		return nil, err
	}
	
	return account, nil
}
