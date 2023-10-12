package migrate

import (
	"database/sql"
	"fmt"
)

// CreateUserTable creates the 'users' table.
func CreateUserTable(db *sql.DB) error {
	createTableSQL := `
	CREATE TABLE users (
		id VARCHAR(255) PRIMARY KEY,
		name VARCHAR(255) UNIQUE NOT NULL,
		email VARCHAR(255) UNIQUE NOT NULL
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
	CREATE TABLE accounts (
		id VARCHAR(255) PRIMARY KEY,
		user_id VARCHAR(255) NOT NULL,
		type VARCHAR(50) NOT NULL,
		provider VARCHAR(50) NOT NULL,
		provider_account_id VARCHAR(255) NOT NULL,
		refresh_token VARCHAR(255),
		access_token VARCHAR(255),
		expires_at DATETIME,
		token_type VARCHAR(50),
		scope VARCHAR(255),
		id_token VARCHAR(255),
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
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
	CREATE TABLE sessions (
		id VARCHAR(255) PRIMARY KEY,
		session_token VARCHAR(255) UNIQUE NOT NULL,
		user_id VARCHAR(255) NOT NULL,
		expires DATETIME,
		is_online BOOLEAN DEFAULT FALSE,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE INDEX idx_sessions_user_id ON sessions(user_id);`

	_, err := db.Exec(createTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create session table: %v", err)
	}

	return nil
}
