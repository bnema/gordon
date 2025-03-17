package queries

import (
	"database/sql"
	"github.com/bnema/gordon/pkg/logger"
)

// GetUsernameByID gets a username by account ID
func GetUsernameByID(database *sql.DB, accountID string) (string, error) {
	var username string
	
	// Query to get the username from the user table via the account table
	query := `
		SELECT user.name 
		FROM user 
		INNER JOIN account ON user.id = account.user_id 
		WHERE account.id = ?
	`
	
	err := database.QueryRow(query, accountID).Scan(&username)
	if err != nil {
		logger.Warn("Failed to get username by account ID", "error", err, "account_id", accountID)
		return "", err
	}
	
	return username, nil
}
