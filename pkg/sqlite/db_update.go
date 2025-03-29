package pkgsqlite

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/charmbracelet/log"
)

// UpdateDatabase compares struct definitions with the actual database schema on disk and updates it accordingly
// Accepts the DBTables struct (or similar) containing the table definitions as fields with `sql` tags.
func UpdateDatabase(dbPath string, dbTablesStruct interface{}) error {
	log.Debug("Starting database schema update check on disk", "path", dbPath)

	// Check if the database file exists BEFORE attempting backup
	_, statErr := os.Stat(dbPath)
	if statErr == nil {
		// File exists, proceed with backup
		log.Debug("Database file exists, attempting backup.", "path", dbPath)
		if err := backupDatabase(dbPath); err != nil {
			// If backup fails on an existing file, that's a real problem
			return fmt.Errorf("failed to backup existing database: %w", err)
		}
	} else if os.IsNotExist(statErr) {
		// File does not exist, skip backup
		log.Debug("Database file doesn't exist, skipping backup.", "path", dbPath)
	} else {
		// Other error during stat (e.g., permissions), return it
		return fmt.Errorf("failed to check database file status before backup: %w", statErr)
	}

	// Open connection to the disk database
	// sql.Open will create the file if it doesn't exist when a connection is made/queried
	diskDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open disk DB for schema update: %w", err)
	}
	defer diskDB.Close()

	// Optional: Ping to ensure the connection is valid and file is created if needed
	if err := diskDB.Ping(); err != nil {
		// It's possible ping fails if the directory doesn't exist yet, ensure dir first.
		// Let's rely on the transaction to create the file implicitly if needed.
		// Commenting out ping as it might add complexity here.
		// return fmt.Errorf("failed to ping disk DB after opening: %w", err)
	}

	// Begin transaction on disk DB
	tx, err := diskDB.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction on disk DB: %w", err)
	}
	// Using defer with a named return error allows checking commit error
	var txErr error
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback() // Attempt rollback on panic
			// Re-panic or handle as needed
			panic(r)
		} else if txErr != nil {
			log.Debug("Rolling back transaction due to error", "error", txErr)
			if rbErr := tx.Rollback(); rbErr != nil {
				log.Error("Failed to rollback transaction", "rollback_error", rbErr)
			}
		}
	}()

	// Update each table schema using reflection on the provided struct
	updated := false
	dbTablesType := reflect.TypeOf(dbTablesStruct)
	dbTablesValue := reflect.ValueOf(dbTablesStruct)
	if dbTablesType.Kind() == reflect.Ptr {
		dbTablesType = dbTablesType.Elem()
		dbTablesValue = dbTablesValue.Elem()
	}

	if dbTablesType.Kind() != reflect.Struct {
		_ = tx.Rollback() // Attempt rollback before returning error
		return fmt.Errorf("UpdateDatabase expects a struct, got %T", dbTablesStruct)
	}
	log.Debug("Reflecting DBTables struct for disk schema update", "type", dbTablesType.Name(), "numFields", dbTablesType.NumField())

	for i := 0; i < dbTablesType.NumField(); i++ {
		field := dbTablesType.Field(i)
		fieldValue := dbTablesValue.Field(i).Interface() // e.g., db.ProxyRoute instance
		sqlTag := field.Tag.Get("sql")

		if sqlTag == "" || sqlTag == "-" {
			log.Debug("Skipping field without sql tag in schema update", "field", field.Name)
			continue // Skip fields without a sql tag or marked to be ignored
		}

		tableName := sqlTag // Use the tag directly as the table name
		log.Debug("Processing field for schema update", "field", field.Name, "tableName", tableName, "structType", reflect.TypeOf(fieldValue).Name())

		var tableUpdated bool
		// Pass the correct tableName from the tag and the actual struct instance (fieldValue)
		tableUpdated, txErr = updateTableSchema(tx, tableName, fieldValue)
		if txErr != nil {
			// updateTableSchema handles creation if table is missing
			// Return the error wrapped, defer will handle rollback
			return fmt.Errorf("failed to update/create table '%s' (from field '%s'): %w", tableName, field.Name, txErr)
		}
		if tableUpdated {
			updated = true
		}
	}

	// Commit transaction - Assign error to txErr for defer check
	txErr = tx.Commit()
	if txErr != nil {
		return fmt.Errorf("failed to commit transaction on disk DB: %w", txErr)
	}

	if updated {
		log.Info("Successfully updated/created database schema on disk")
	} else {
		log.Debug("Database schema on disk is up-to-date")
	}
	return nil
}

// updateTableSchema updates a single table's schema based on struct definition
// Returns true if the schema was updated or created, false otherwise.
func updateTableSchema(tx *sql.Tx, tableName string, structType interface{}) (bool, error) {
	log.Debug("Starting schema update check for table", "table", tableName)
	updated := false
	// Get existing columns
	existingColumns, err := getTableColumns(tx, tableName)
	if err != nil {
		// If the error is "no such table", try creating it
		if IsNoSuchTableError(err) {
			log.Info("Table does not exist (getTableColumns returned error), attempting to create", "table", tableName)
			if createErr := CreateTableInTx(tx, structType, tableName); createErr != nil {
				return false, fmt.Errorf("failed to create missing table %s: %w", tableName, createErr)
			}
			log.Info("Successfully created table", "table", tableName)
			return true, nil // Table was created (updated=true)
		}
		// Otherwise, it's a different error when getting columns
		return false, fmt.Errorf("failed to get existing columns for table %s: %w", tableName, err)
	}

	// Check if getTableColumns succeeded but found no columns.
	// Treat this as if the table needs creation.
	if len(existingColumns) == 0 {
		log.Info("Table exists according to PRAGMA but has no columns, treating as missing/invalid, attempting to create", "table", tableName)
		// Attempt to drop the potentially problematic empty/invalid table first (optional but safer)
		// We can ignore errors here, as it might not actually exist for DROP.
		_, _ = tx.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))

		// Now create it properly
		if createErr := CreateTableInTx(tx, structType, tableName); createErr != nil {
			return false, fmt.Errorf("failed to create missing/invalid table %s: %w", tableName, createErr)
		}
		log.Info("Successfully recreated table", "table", tableName)
		return true, nil // Table was created/recreated (updated=true)
	}

	log.Debug("Existing columns found", "table", tableName, "columns", existingColumns)

	// Get struct fields
	structFields := getStructFields(structType)
	log.Debug("Struct fields found", "table", tableName, "fields", structFields)

	// Compare and add missing columns
	for fieldName, fieldType := range structFields {
		if _, exists := existingColumns[fieldName]; !exists {
			sqlType := convertGoTypeToSQLType(fieldType) // Use the helper function
			query := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tableName, fieldName, sqlType)

			log.Debug("Attempting to add missing column", "table", tableName, "column", fieldName, "type", sqlType, "query", query)
			if _, err := tx.Exec(query); err != nil {
				if isDuplicateColumnError(err) {
					log.Debug("Column already exists (detected via duplicate error), skipping", "table", tableName, "column", fieldName)
				} else {
					log.Error("Failed to execute ALTER TABLE", "table", tableName, "column", fieldName, "query", query, "error", err)
					return false, fmt.Errorf("failed to add column %s to table %s: %w", fieldName, tableName, err)
				}
			} else {
				log.Info("Successfully added column", "table", tableName, "column", fieldName)
				updated = true // Column was successfully added
			}
		}
	}

	log.Debug("Finished schema update check for table", "table", tableName, "schema_was_modified", updated)
	return updated, nil
}

// getTableColumns returns a map of existing column names in the table
func getTableColumns(tx *sql.Tx, tableName string) (map[string]bool, error) {
	columns := make(map[string]bool)

	query := fmt.Sprintf("PRAGMA table_info(%s)", tableName)
	rows, err := tx.Query(query)
	if err != nil {
		// Specifically check if the error indicates "no such table" from the PRAGMA itself
		if IsNoSuchTableError(err) {
			// Return the specific error type so the caller can handle it
			return nil, err
		}
		// Otherwise, wrap the generic error
		return nil, fmt.Errorf("failed to query table info for %s: %w", tableName, err)
	}
	defer rows.Close()

	scanFailed := false // Flag to track scan errors
	for rows.Next() {
		var (
			cid     int
			name    string
			colType string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &colType, &notnull, &dflt, &pk); err != nil {
			log.Error("Failed to scan column info row", "table", tableName, "error", err)
			scanFailed = true // Set flag on scan error
			break             // Stop processing rows on error
		}
		columns[name] = true
	}

	// Check for errors after the loop (including scan errors)
	if err := rows.Err(); err != nil {
		// Prioritize returning the specific scan error if it happened
		if scanFailed {
			// We already logged the scan error, return a generic iteration error
			return nil, fmt.Errorf("error scanning table info for %s: %w", tableName, err)
		}
		// Otherwise, return the iteration error from rows.Err()
		return nil, fmt.Errorf("error iterating table info for %s: %w", tableName, err)
	}

	// If scan failed, even if rows.Err() is nil, return an error
	if scanFailed {
		return nil, fmt.Errorf("failed to scan one or more column info rows for table %s", tableName)
	}

	// If we get here, PRAGMA succeeded and iteration/scanning completed without error.
	// It's safe to return the map (which might be empty) and nil error.
	return columns, nil
}

// getStructFields returns a map of field names to their types from a struct
func getStructFields(structType interface{}) map[string]string {
	fields := make(map[string]string)
	t := reflect.TypeOf(structType)

	// If it's a pointer, get the element type
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Ensure it's actually a struct before proceeding
	if t.Kind() != reflect.Struct {
		log.Warn("getStructFields called with non-struct type", "type", t.Name())
		return fields // Return empty map
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		sqlTag := field.Tag.Get("sql")
		if sqlTag == "" {
			continue
		}

		// Get the SQL column name from the tag
		parts := strings.Split(sqlTag, ",")
		sqlName := parts[0]

		// Store the field type
		fields[sqlName] = field.Type.Name()
	}

	return fields
}

// backupDatabase creates a backup of the database file
// Assumes the dbPath already exists when this is called.
func backupDatabase(dbPath string) error {
	// Create backup directory if it doesn't exist
	backupDir := filepath.Join(filepath.Dir(dbPath), "backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return fmt.Errorf("failed to create backup directory: %w", err)
	}

	// Generate backup filename with timestamp
	timestamp := time.Now().Format("20060102_150405")
	backupFile := filepath.Join(backupDir, fmt.Sprintf("%s_%s.db", filepath.Base(dbPath), timestamp))

	// Read the database file
	data, err := os.ReadFile(dbPath)
	if err != nil {
		// This shouldn't happen now based on the check in UpdateDatabase, but check again.
		if os.IsNotExist(err) {
			log.Warn("backupDatabase called but file vanished", "path", dbPath)
			return nil // File disappeared between check and read? Treat as success.
		}
		return fmt.Errorf("failed to read database file for backup: %w", err)
	}

	// Write to backup file
	if err := os.WriteFile(backupFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write backup file: %w", err)
	}

	log.Info("Created database backup", "backup_file", backupFile)
	return nil
}

// isDuplicateColumnError checks if the error is due to a column already existing (SQLite specific)
func isDuplicateColumnError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

// IsNoSuchTableError checks if the error is due to a table not existing (Exported: changed i to I)
func IsNoSuchTableError(err error) bool {
	// Check for the specific SQLite error message
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such table")
}
