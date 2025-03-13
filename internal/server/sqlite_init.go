package server

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bnema/gordon/internal/db"
	"github.com/charmbracelet/log"
	_ "modernc.org/sqlite"
)

const (
	DBFilename = "sqlite.db"
	// Auto-save interval in minutes
	AutoSaveInterval = 5
)

func (a *App) getDiskDBFilePath() string {
	DiskDBfilepath := filepath.Join(a.DBDir, a.DBFilename)
	// if a.DBDir is empty return log "Set a path using storageDir in the config file"
	log.Debug("DB file path", "path", DiskDBfilepath)
	return DiskDBfilepath
}

// ensureDBDir ensures that the database directory exists.
func (a *App) ensureDBDir() error {
	if _, err := os.Stat(a.DBDir); os.IsNotExist(err) {
		log.Debug("Creating database directory", "dir", a.DBDir)
		if err := os.MkdirAll(a.DBDir, 0755); err != nil {
			return err
		}
	}
	return nil
}

// checkDBFile checks if the database file exists.
func (a *App) checkDBFile(dbPath string) (bool, error) {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		log.Debug("Database file does not exist", "path", dbPath)
		return false, nil
	} else if err != nil {
		return false, err
	}

	// File exists, but let's also check if the tables exist
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return false, fmt.Errorf("failed to open DB to check tables: %w", err)
	}
	defer db.Close()

	// Check if domain table exists
	var tableCount int
	err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='domain'").Scan(&tableCount)
	if err != nil {
		return false, fmt.Errorf("failed to check for domain table: %w", err)
	}

	if tableCount == 0 {
		log.Debug("Database file exists but domain table is missing, will reinitialize")
		// The file exists but doesn't have the required tables, treat as non-existent
		if err := os.Remove(dbPath); err != nil {
			return false, fmt.Errorf("failed to remove corrupted DB file: %w", err)
		}
		return false, nil
	}

	log.Debug("Database file exists and contains required tables", "path", dbPath)
	return true, nil
}

// InitializeDB initializes the database in memory and loads data from disk if available
func InitializeDB(a *App) (*sql.DB, error) {
	log.Debug("Initializing in-memory database")
	if err := a.ensureDBDir(); err != nil {
		return nil, fmt.Errorf("failed to ensure DB directory: %w", err)
	}

	diskDBPath := a.getDiskDBFilePath()
	a.DBPath = diskDBPath

	// Create in-memory database connection
	log.Debug("Opening in-memory SQLite database")
	memDB, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		return nil, fmt.Errorf("failed to open in-memory DB: %w", err)
	}

	// Test that the in-memory database is working
	log.Debug("Testing in-memory database connection")
	if err := memDB.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping in-memory database: %w", err)
	}

	// Check if disk database exists
	dbExists, err := a.checkDBFile(diskDBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to check DB file: %w", err)
	}

	if !dbExists {
		log.Debug("Database needs bootstrapping", "path", diskDBPath)
		// Create tables in memory
		log.Debug("Creating tables in memory")
		if err := bootstrapDB(memDB); err != nil {
			return nil, fmt.Errorf("failed to bootstrap in-memory DB: %w", err)
		}

		// Save the initialized in-memory database to disk
		log.Debug("Saving initialized in-memory database to disk")
		if err := saveMemDBToDisk(memDB, diskDBPath); err != nil {
			return nil, fmt.Errorf("failed to save initial in-memory DB to disk: %w", err)
		}

		log.Debug("Initialized new in-memory database and saved to disk")
	} else {
		// Load existing database from disk into memory
		log.Debug("Loading existing database from disk into memory", "path", diskDBPath)
		if err := loadDiskDBToMemory(diskDBPath, memDB); err != nil {
			return nil, fmt.Errorf("failed to load disk DB into memory: %w", err)
		}

		log.Debug("Successfully loaded disk database into memory")
	}

	a.DB = memDB

	if dbExists {
		// Populate the struct tables from the database
		log.Debug("Populating struct tables from existing database")
		if err := PopulateStructWithTables(a); err != nil {
			return nil, err
		}
	}

	// Create a context for the auto-save routine that will be canceled when the app is shutting down
	a.DBSaveCtx, a.DBSaveCancel = context.WithCancel(context.Background())

	// Start auto-save routine
	log.Debug("Starting auto-save routine with interval of", "minutes", AutoSaveInterval)
	go autoSaveDB(a.DBSaveCtx, a, AutoSaveInterval)

	return memDB, nil
}

// loadDiskDBToMemory loads the database from disk into memory
func loadDiskDBToMemory(diskDBPath string, memDB *sql.DB) error {
	log.Debug("Opening disk database for loading", "path", diskDBPath)
	// Open the disk database
	diskDB, err := sql.Open("sqlite", diskDBPath)
	if err != nil {
		return fmt.Errorf("failed to open disk DB for loading: %w", err)
	}
	defer diskDB.Close()

	// Backup disk database to memory
	// First, get all table names
	log.Debug("Retrieving table names from disk database")
	rows, err := diskDB.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return fmt.Errorf("failed to get table names: %w", err)
	}
	defer rows.Close()

	// Begin transaction for memory DB
	log.Debug("Beginning transaction on memory database")
	memTx, err := memDB.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction on memory DB: %w", err)
	}
	defer func() {
		if err != nil {
			log.Debug("Rolling back memory database transaction due to error")
			memTx.Rollback()
		}
	}()

	tableCount := 0
	// For each table, create it in memory and copy data
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return fmt.Errorf("failed to scan table name: %w", err)
		}
		tableCount++

		log.Debug("Processing table", "name", tableName)

		// Get table schema
		var createStmt string
		err := diskDB.QueryRow(fmt.Sprintf("SELECT sql FROM sqlite_master WHERE type='table' AND name='%s'", tableName)).Scan(&createStmt)
		if err != nil {
			return fmt.Errorf("failed to get create statement for table %s: %w", tableName, err)
		}

		// Create table in memory
		log.Debug("Creating table in memory", "name", tableName)
		_, err = memTx.Exec(createStmt)
		if err != nil {
			return fmt.Errorf("failed to create table %s in memory: %w", tableName, err)
		}

		// Get all data from disk table
		log.Debug("Retrieving data from disk table", "name", tableName)
		dataRows, err := diskDB.Query(fmt.Sprintf("SELECT * FROM %s", tableName))
		if err != nil {
			return fmt.Errorf("failed to query data from table %s: %w", tableName, err)
		}
		defer dataRows.Close()

		// Get column names
		columns, err := dataRows.Columns()
		if err != nil {
			return fmt.Errorf("failed to get columns for table %s: %w", tableName, err)
		}
		log.Debug("Table columns", "name", tableName, "columns", columns)

		// Prepare insert statement for memory DB
		placeholders := make([]string, len(columns))
		for i := range placeholders {
			placeholders[i] = "?"
		}
		insertStmt, err := memTx.Prepare(fmt.Sprintf("INSERT INTO %s VALUES (%s)", tableName, joinStrings(placeholders, ",")))
		if err != nil {
			return fmt.Errorf("failed to prepare insert statement for table %s: %w", tableName, err)
		}
		defer insertStmt.Close()

		// Copy data row by row
		rowCount := 0
		for dataRows.Next() {
			// Create a slice of interface{} to hold the values
			values := make([]interface{}, len(columns))
			valuePtrs := make([]interface{}, len(columns))
			for i := range values {
				valuePtrs[i] = &values[i]
			}

			// Scan the row into the slice
			if err := dataRows.Scan(valuePtrs...); err != nil {
				return fmt.Errorf("failed to scan row from table %s: %w", tableName, err)
			}

			// Insert into memory DB
			_, err = insertStmt.Exec(values...)
			if err != nil {
				return fmt.Errorf("failed to insert row into memory table %s: %w", tableName, err)
			}
			rowCount++
		}
		log.Debug("Copied rows to memory", "table", tableName, "count", rowCount)

		if err := dataRows.Err(); err != nil {
			return fmt.Errorf("error iterating rows for table %s: %w", tableName, err)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating table names: %w", err)
	}

	// Commit transaction
	log.Debug("Committing transaction to memory database")
	if err := memTx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction to memory DB: %w", err)
	}

	log.Debug("Successfully loaded database from disk to memory", "tables", tableCount)
	return nil
}

// saveMemDBToDisk saves the in-memory database to disk
func saveMemDBToDisk(memDB *sql.DB, diskDBPath string) error {
	log.Debug("Saving in-memory database to disk", "path", diskDBPath)

	// Create a backup of the existing file if it exists
	if _, err := os.Stat(diskDBPath); err == nil {
		backupPath := diskDBPath + ".bak"
		log.Debug("Creating backup of existing database file", "backup", backupPath)
		if err := os.Rename(diskDBPath, backupPath); err != nil {
			log.Warn("Failed to create backup of database file", "error", err)
			// Continue anyway, as this is just a backup
		} else {
			log.Debug("Created backup of database file", "backup", backupPath)
		}
	}

	// Open the disk database
	log.Debug("Opening disk database for saving")
	diskDB, err := sql.Open("sqlite", diskDBPath)
	if err != nil {
		return fmt.Errorf("failed to open disk DB for saving: %w", err)
	}
	defer diskDB.Close()

	// Get all table names from memory DB
	log.Debug("Retrieving table names from memory database")
	rows, err := memDB.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return fmt.Errorf("failed to get table names from memory: %w", err)
	}
	defer rows.Close()

	// Begin transaction for disk DB
	log.Debug("Beginning transaction on disk database")
	diskTx, err := diskDB.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction on disk DB: %w", err)
	}
	defer func() {
		if err != nil {
			log.Debug("Rolling back disk database transaction due to error")
			diskTx.Rollback()
		}
	}()

	tableCount := 0
	// For each table, create it on disk and copy data
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return fmt.Errorf("failed to scan table name: %w", err)
		}
		tableCount++

		log.Debug("Processing table for saving", "name", tableName)

		// Get table schema
		var createStmt string
		err := memDB.QueryRow(fmt.Sprintf("SELECT sql FROM sqlite_master WHERE type='table' AND name='%s'", tableName)).Scan(&createStmt)
		if err != nil {
			return fmt.Errorf("failed to get create statement for table %s: %w", tableName, err)
		}

		// Create table on disk
		log.Debug("Creating table on disk", "name", tableName)
		_, err = diskTx.Exec(createStmt)
		if err != nil {
			return fmt.Errorf("failed to create table %s on disk: %w", tableName, err)
		}

		// Get all data from memory table
		log.Debug("Retrieving data from memory table", "name", tableName)
		dataRows, err := memDB.Query(fmt.Sprintf("SELECT * FROM %s", tableName))
		if err != nil {
			return fmt.Errorf("failed to query data from memory table %s: %w", tableName, err)
		}
		defer dataRows.Close()

		// Get column names
		columns, err := dataRows.Columns()
		if err != nil {
			return fmt.Errorf("failed to get columns for table %s: %w", tableName, err)
		}

		// Prepare insert statement for disk DB
		placeholders := make([]string, len(columns))
		for i := range placeholders {
			placeholders[i] = "?"
		}
		insertStmt, err := diskTx.Prepare(fmt.Sprintf("INSERT INTO %s VALUES (%s)", tableName, joinStrings(placeholders, ",")))
		if err != nil {
			return fmt.Errorf("failed to prepare insert statement for table %s: %w", tableName, err)
		}
		defer insertStmt.Close()

		// Copy data row by row
		rowCount := 0
		for dataRows.Next() {
			// Create a slice of interface{} to hold the values
			values := make([]interface{}, len(columns))
			valuePtrs := make([]interface{}, len(columns))
			for i := range values {
				valuePtrs[i] = &values[i]
			}

			// Scan the row into the slice
			if err := dataRows.Scan(valuePtrs...); err != nil {
				return fmt.Errorf("failed to scan row from memory table %s: %w", tableName, err)
			}

			// Insert into disk DB
			_, err = insertStmt.Exec(values...)
			if err != nil {
				return fmt.Errorf("failed to insert row into disk table %s: %w", tableName, err)
			}
			rowCount++
		}
		log.Debug("Copied rows to disk", "table", tableName, "count", rowCount)

		if err := dataRows.Err(); err != nil {
			return fmt.Errorf("error iterating rows for table %s: %w", tableName, err)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating table names: %w", err)
	}

	// Commit transaction
	log.Debug("Committing transaction to disk database")
	if err := diskTx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction to disk DB: %w", err)
	}

	log.Debug("Successfully saved in-memory database to disk", "tables", tableCount)
	return nil
}

// autoSaveDB periodically saves the in-memory database to disk
func autoSaveDB(ctx context.Context, a *App, intervalMinutes int) {
	ticker := time.NewTicker(time.Duration(intervalMinutes) * time.Minute)
	defer ticker.Stop()

	log.Debug("Auto-save routine started", "interval_minutes", intervalMinutes)

	for {
		select {
		case <-ticker.C:
			log.Debug("Auto-saving in-memory database to disk")
			if err := saveMemDBToDisk(a.DB, a.DBPath); err != nil {
				log.Error("Failed to auto-save in-memory database to disk", "error", err)
			} else {
				log.Debug("Auto-save completed successfully")
			}
		case <-ctx.Done():
			log.Debug("Auto-save routine stopping due to context cancellation")
			return
		}
	}
}

// Helper function to join strings with a separator
func joinStrings(strings []string, separator string) string {
	if len(strings) == 0 {
		return ""
	}
	result := strings[0]
	for i := 1; i < len(strings); i++ {
		result += separator + strings[i]
	}
	return result
}

// bootstrapDB creates the initial database schema
func bootstrapDB(db *sql.DB) error {
	log.Debug("Bootstrapping database with initial schema")

	// Begin a transaction
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("error beginning transaction: %w", err)
	}

	// Create tables
	log.Debug("Creating database tables")
	_, err = tx.Exec(`
		CREATE TABLE IF NOT EXISTS user (
			id TEXT PRIMARY KEY,
			name TEXT,
			email TEXT
		);
		CREATE TABLE IF NOT EXISTS account (
			id TEXT PRIMARY KEY,
			user_id TEXT,
			FOREIGN KEY (user_id) REFERENCES user(id)
		);
		CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			account_id TEXT,
			browser_info TEXT,
			access_token TEXT,
			expires TEXT,
			is_online INTEGER,
			FOREIGN KEY (account_id) REFERENCES account(id)
		);
		CREATE TABLE IF NOT EXISTS provider (
			id TEXT PRIMARY KEY,
			account_id TEXT,
			name TEXT,
			login TEXT,
			avatar_url TEXT,
			profile_url TEXT,
			email TEXT,
			FOREIGN KEY (account_id) REFERENCES account(id)
		);
		CREATE TABLE IF NOT EXISTS clients (
			id TEXT PRIMARY KEY,
			account_id TEXT,
			os TEXT,
			ip TEXT,
			hostname TEXT,
			expires TEXT,
			FOREIGN KEY (account_id) REFERENCES account(id)
		);
		CREATE TABLE IF NOT EXISTS domain (
			id TEXT PRIMARY KEY,
			name TEXT,
			account_id TEXT,
			created_at TEXT,
			updated_at TEXT,
			FOREIGN KEY (account_id) REFERENCES account(id)
		);
		CREATE TABLE IF NOT EXISTS certificate (
			id TEXT PRIMARY KEY,
			domain_id TEXT,
			cert_file TEXT,
			key_file TEXT,
			issued_at TEXT,
			expires_at TEXT,
			issuer TEXT,
			status TEXT,
			FOREIGN KEY (domain_id) REFERENCES domain(id)
		);
		CREATE TABLE IF NOT EXISTS proxy_route (
			id TEXT PRIMARY KEY,
			domain_id TEXT,
			container_id TEXT,
			container_ip TEXT,
			container_port TEXT,
			protocol TEXT,
			path TEXT,
			active INTEGER,
			created_at TEXT,
			updated_at TEXT,
			FOREIGN KEY (domain_id) REFERENCES domain(id)
		);
	`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("error creating tables: %w", err)
	}

	// Commit the transaction
	log.Debug("Committing transaction with initial schema")
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("error committing transaction: %w", err)
	}

	log.Debug("Database bootstrap completed successfully")
	return nil
}

type DBTable interface {
	ScanFromRows(rows *sql.Rows) error
}

type UserWrapper struct {
	*db.User
}

type AccountWrapper struct {
	*db.Account
}

type ProviderWrapper struct {
	*db.Provider
}

type SessionsWrapper struct {
	*db.Sessions
}

type ClientsWrapper struct {
	*db.Clients
}

type DomainWrapper struct {
	*db.Domain
}

type CertificateWrapper struct {
	*db.Certificate
}

type ProxyRouteWrapper struct {
	*db.ProxyRoute
}

func populateTableFromDB(a *App, query string, table DBTable) error {
	rows, err := a.DB.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		if err := table.ScanFromRows(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Implementation of DBTable for User, Account, Provider, Sessions
func (u *UserWrapper) ScanFromRows(rows *sql.Rows) error {
	return rows.Scan(&u.ID, &u.Name, &u.Email)
}

func (a *AccountWrapper) ScanFromRows(rows *sql.Rows) error {
	return rows.Scan(&a.ID, &a.UserID)
}

func (p *ProviderWrapper) ScanFromRows(rows *sql.Rows) error {
	return rows.Scan(&p.ID, &p.AccountID, &p.Name, &p.Login, &p.AvatarURL, &p.ProfileURL, &p.Email)
}

func (s *SessionsWrapper) ScanFromRows(rows *sql.Rows) error {
	return rows.Scan(&s.ID, &s.AccountID, &s.AccessToken, &s.BrowserInfo, &s.Expires, &s.IsOnline)
}

func (c *ClientsWrapper) ScanFromRows(rows *sql.Rows) error {
	return rows.Scan(&c.ID, &c.AccountID, &c.OS, &c.IP, &c.Hostname, &c.Expires)
}

func (d *DomainWrapper) ScanFromRows(rows *sql.Rows) error {
	return rows.Scan(&d.ID, &d.Name, &d.AccountID, &d.CreatedAt, &d.UpdatedAt)
}

func (c *CertificateWrapper) ScanFromRows(rows *sql.Rows) error {
	return rows.Scan(&c.ID, &c.DomainID, &c.CertFile, &c.KeyFile, &c.IssuedAt, &c.ExpiresAt, &c.Issuer, &c.Status)
}

func (p *ProxyRouteWrapper) ScanFromRows(rows *sql.Rows) error {
	return rows.Scan(&p.ID, &p.DomainID, &p.ContainerID, &p.ContainerIP, &p.ContainerPort, &p.Protocol, &p.Path, &p.Active)
}

// PopulateStructWithTables updates db struct with tables
func PopulateStructWithTables(a *App) error {
	// Populate the User table
	userWrapper := &UserWrapper{User: &a.DBTables.User}
	if err := populateTableFromDB(a, "SELECT id, name, email FROM user", userWrapper); err != nil {
		return err
	}

	// Populate the Account table
	accountWrapper := &AccountWrapper{Account: &a.DBTables.Account}
	if err := populateTableFromDB(a, "SELECT id, user_id FROM account", accountWrapper); err != nil {
		return err
	}

	// Populate the Provider table
	providerWrapper := &ProviderWrapper{Provider: &a.DBTables.Provider}
	if err := populateTableFromDB(a, "SELECT id, account_id, name, login, avatar_url, profile_url, email FROM provider", providerWrapper); err != nil {
		return err
	}

	// Populate the Sessions table
	sessionsWrapper := &SessionsWrapper{Sessions: &a.DBTables.Sessions}
	if err := populateTableFromDB(a, "SELECT id, account_id, browser_info, access_token, expires, is_online FROM sessions", sessionsWrapper); err != nil {
		return err
	}

	// Populate the Clients table
	clientsWrapper := &ClientsWrapper{Clients: &a.DBTables.Clients}
	if err := populateTableFromDB(a, "SELECT id, account_id, os, ip, hostname, expires FROM clients", clientsWrapper); err != nil {
		return err
	}

	// Populate the Domain table
	domainWrapper := &DomainWrapper{Domain: &a.DBTables.Domain}
	if err := populateTableFromDB(a, "SELECT id, name, account_id, created_at, updated_at FROM domain", domainWrapper); err != nil {
		return err
	}

	// Populate the Certificate table
	certificateWrapper := &CertificateWrapper{Certificate: &a.DBTables.Certificate}
	if err := populateTableFromDB(a, "SELECT id, domain_id, cert_file, key_file, issued_at, expires_at, issuer, status FROM certificate", certificateWrapper); err != nil {
		return err
	}

	// Populate the ProxyRoute table
	proxyRouteWrapper := &ProxyRouteWrapper{ProxyRoute: &a.DBTables.ProxyRoute}
	if err := populateTableFromDB(a, "SELECT id, domain_id, container_id, container_ip, container_port, protocol, path, active FROM proxy_route", proxyRouteWrapper); err != nil {
		return err
	}

	return nil
}
