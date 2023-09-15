package app

import (
	"database/sql"
	"os"
	"testing"
)

// test for CloseAndBackupDB with database modifications (checksum should be different and backup file should be created)
func TestCloseAndBackupDBWithModifications(t *testing.T) {
	// Ensure the directory exists
	err := os.MkdirAll("./tmp/data", 0755)
	if err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}

	// Create a temporary file for testing
	tempFile, err := os.CreateTemp("./tmp/data", "sqlite.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tempFile.Close()

	app := &App{
		DBFilename: tempFile.Name(),
	}

	// Initialize in-memory database
	memDb, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open in-memory database: %v", err)
	}
	defer memDb.Close()

	// Create table and insert some data
	_, err = memDb.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)")
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	_, err = memDb.Exec("INSERT INTO users (name) VALUES ('Alice'), ('Bob')")
	if err != nil {
		t.Fatalf("Failed to insert data: %v", err)
	}

	// Generate initial checksum
	initialChecksum, err := GenerateDBChecksum(memDb)
	if err != nil {
		t.Fatalf("Failed to generate initial checksum: %v", err)
	}

	// Validate: Checksum should remain the same
	finalChecksum, err := GenerateDBChecksum(memDb)
	if err != nil {
		t.Fatalf("Failed to generate final checksum: %v", err)
	}
	if initialChecksum != finalChecksum {
		t.Fatalf("Checksum mismatch. Expected: %s, Got: %s", initialChecksum, finalChecksum)
	}

	// Modify the database
	_, err = memDb.Exec("INSERT INTO users (name) VALUES ('Charlie')")
	if err != nil {
		t.Fatalf("Failed to insert data: %v", err)
	}

	// Validate: Checksum should be different
	finalChecksum, err = GenerateDBChecksum(memDb)
	if err != nil {
		t.Fatalf("Failed to generate final checksum: %v", err)
	}
	if initialChecksum == finalChecksum {
		t.Fatalf("Checksum mismatch. Expected: %s, Got: %s", initialChecksum, finalChecksum)
	}

	// Test: CloseAndBackupDB with modification
	err = CloseAndBackupDB(app, memDb)
	if err != nil {
		t.Fatalf("Failed in CloseAndBackupDB: %v", err)
	}

}

// test for CloseAndBackupDB with NO database modifications (checksum should remain the same and no backup file should be created)
func TestCloseAndBackupDBWithNoModifications(t *testing.T) {
	// Ensure the directory exists
	err := os.MkdirAll("./tmp/data", 0755)
	if err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}

	// Create a temporary file for testing
	tempFile, err := os.CreateTemp("./tmp/data", "sqlite.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tempFile.Close()

	app := &App{
		DBFilename: tempFile.Name(),
	}

	// Initialize in-memory database
	memDb, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open in-memory database: %v", err)
	}
	defer memDb.Close()

	// Create table and insert some data
	_, err = memDb.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)")
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	_, err = memDb.Exec("INSERT INTO users (name) VALUES ('Alice'), ('Bob')")
	if err != nil {
		t.Fatalf("Failed to insert data: %v", err)
	}

	// Generate initial checksum
	initialChecksum, err := GenerateDBChecksum(memDb)
	if err != nil {
		t.Fatalf("Failed to generate initial checksum: %v", err)
	}

	// Validate: Checksum should remain the same
	finalChecksum, err := GenerateDBChecksum(memDb)
	if err != nil {
		t.Fatalf("Failed to generate final checksum: %v", err)
	}
	if initialChecksum != finalChecksum {
		t.Fatalf("Checksum mismatch. Expected: %s, Got: %s", initialChecksum, finalChecksum)
	}

	// Test: CloseAndBackupDB with modification
	err = CloseAndBackupDB(app, memDb)
	if err != nil {
		t.Fatalf("Failed in CloseAndBackupDB: %v", err)
	}

}
