package queries

import (
	"fmt"

	"github.com/bnema/gordon/internal/server"
)

// InspectInMemoryDB inspects the in-memory database. (for debug purpose)
func InspectInMemoryDB(a *server.App) error {
	memDb := a.DB
	// Query the sqlite_master table to get a list of all tables
	rows, err := memDb.Query("SELECT name FROM sqlite_master WHERE type='table'")
	fmt.Println("rows", rows)
	if err != nil {
		return err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return err
		}
		tables = append(tables, tableName)
	}

	if err := rows.Err(); err != nil {
		return err
	}

	// Print the tables
	fmt.Println("Tables in the in-memory database:")
	for _, table := range tables {
		fmt.Println(table)
	}
	// Print the content of the users table
	userRows, err := memDb.Query("SELECT * FROM user")
	if err != nil {
		return err
	}
	defer userRows.Close()

	fmt.Println("\nContent of the 'user' table:")
	for userRows.Next() {
		// Assuming users table has columns: id, name
		var id int
		var name string
		if err := userRows.Scan(&id, &name); err != nil {
			return err
		}
		fmt.Printf("ID: %d, Name: %s\n", id, name)
	}

	// Print the content of accounts table
	accountRows, err := memDb.Query("SELECT * FROM account")
	if err != nil {
		return err
	}

	fmt.Println("\nContent of the 'account' table:")
	for accountRows.Next() {
		// Assuming accounts table has columns: id, name, user_id
		var id int
		var name string
		var userID int
		if err := accountRows.Scan(&id, &name, &userID); err != nil {
			return err
		}
		fmt.Printf("ID: %d, Name: %s, UserID: %d\n", id, name, userID)
	}

	return nil
}
