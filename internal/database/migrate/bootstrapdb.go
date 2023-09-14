package migrate

import (
	"database/sql"
	"fmt"
)

// createUserTable creates the 'user' table in the provided SQLite database.
func CreateUserTable(db *sql.DB) error {
	createTableSQL := `
	CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL,
		password TEXT NOT NULL,  -- This should be hashed and salted in a real-world scenario.
		oauth_token TEXT
	);`

	_, err := db.Exec(createTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create user table: %v", err)
	}

	return nil
}
