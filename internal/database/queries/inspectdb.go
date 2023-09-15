package queries

import (
	"database/sql"
	"fmt"
)

// InspectInMemoryDB inspects the in-memory database. (for debug purpose)
func InspectInMemoryDB(memDb *sql.DB) error {
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

	// If you want to see the content of a specific table (e.g., users):
	userRows, err := memDb.Query("SELECT * FROM users")
	if err != nil {
		return err
	}
	defer userRows.Close()

	fmt.Println("\nContent of the 'users' table:")
	for userRows.Next() {
		// Assuming users table has columns: id, name
		var id int
		var name string
		if err := userRows.Scan(&id, &name); err != nil {
			return err
		}
		fmt.Printf("ID: %d, Name: %s\n", id, name)
	}

	return nil
}
