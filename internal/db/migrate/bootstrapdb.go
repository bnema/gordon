package migrate

import (
	"database/sql"
	"fmt"
)

// CreateUserTable creates the 'users' table.
func CreateUserTable(db *sql.DB) error {
	createTableSQL := `
	CREATE TABLE user (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    email TEXT NOT NULL UNIQUE
);`

	_, err := db.Exec(createTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create user table: %v", err)
	}

	return nil
}

// CreateAccountTable creates the 'accounts' table.
func CreateAccountTable(db *sql.DB) error {
	createTableSQL := `
	CREATE TABLE account (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER UNIQUE,
    FOREIGN KEY (user_id) REFERENCES user(id)
);`

	_, err := db.Exec(createTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create account table: %v", err)
	}

	return nil
}

// CreateSessionTable creates the 'sessions' table.
func CreateSessionTable(db *sql.DB) error {
	createTableSQL := `
	CREATE TABLE session (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER,
    session_token TEXT NOT NULL,
    expires TEXT NOT NULL,
    is_online BOOLEAN,
    FOREIGN KEY (account_id) REFERENCES account(id)
);`

	_, err := db.Exec(createTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create session table: %v", err)
	}

	return nil
}

// CreateProviderTable creates the 'providers' table.
func CreateProviderTable(db *sql.DB) error {
	createTableSQL := `
	CREATE TABLE provider (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER,
    name TEXT NOT NULL,
    access_token TEXT NOT NULL,
    refresh_token TEXT,
    expires TEXT NOT NULL,
    FOREIGN KEY (account_id) REFERENCES account(id)
);`

	_, err := db.Exec(createTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create provider table: %v", err)
	}

	return nil
}
