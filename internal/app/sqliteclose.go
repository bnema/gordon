package app

import (
	"database/sql"
	"fmt"
	"os"
	"reflect"
)

// GetTableNames returns a list of table names in the database.
func getTableNames(a *App) []string {
	var tableNames []string
	v := reflect.ValueOf(a.DBTables)
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		sqlTag := field.Tag.Get("sql")
		if sqlTag != "" {
			tableNames = append(tableNames, sqlTag)
		} else {
			// Use the field name as a fallback
			tableNames = append(tableNames, field.Name)
		}
	}
	return tableNames
}

// CloseAndBackupDB closes the in-memory database and backup the database file if there is any modification.
func CloseAndBackupDB(a *App, memDb *sql.DB) error {
	currentChecksum, err := GenerateDBChecksum(memDb, a)
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

		tables := getTableNames(a)

		for _, table := range tables {
			query := fmt.Sprintf("CREATE TABLE diskdb.%s AS SELECT * FROM main.%s", table, table)
			_, err = memDb.Exec(query)
			if err != nil {
				return err
			}
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
