package app

import (
	"database/sql"
	"fmt"
	"os"
)

// GetTableNames returns a list of table names in the database.
func GetTableNames(db *sql.DB) ([]string, error) {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, err
		}
		tables = append(tables, tableName)
	}

	return tables, nil
}

// CloseAndBackupDB closes the in-memory database and backup the database file if there is any modification.
func CloseAndBackupDB(a *App, memDb *sql.DB) error {
	currentChecksum, err := GenerateDBChecksum(memDb)
	if err != nil {
		return err
	}
	fmt.Printf("Current checksum: %s\n", currentChecksum)
	fmt.Printf("Initial checksum: %s\n", a.InitialChecksum)
	if currentChecksum != a.InitialChecksum {
		fmt.Printf("Modification has been found in the database. Current checksum: %s\n", currentChecksum)

		// Backup the current db file
		backupPath := a.GetDiskDBFilePath() + ".backup"
		if err := os.Rename(a.GetDiskDBFilePath(), backupPath); err != nil {
			return err
		}
		fmt.Printf("Backup file has been created: %s\n", backupPath)

		// Save the in-memory database to disk
		_, err = memDb.Exec("ATTACH DATABASE '" + a.GetDiskDBFilePath() + "' AS diskdb")
		if err != nil {
			return err
		}

		_, err = memDb.Exec("CREATE TABLE diskdb.users AS SELECT * FROM main.users")
		if err != nil {
			return err
		}

		_, err = memDb.Exec("DETACH DATABASE diskdb")
		if err != nil {
			return err
		}
	} else {
		fmt.Printf("No modification has been found in the database.\n")
	}

	if err := memDb.Close(); err != nil {
		return err
	}
	fmt.Printf("In-memory database has been closed gracefully.\n")

	return nil
}
