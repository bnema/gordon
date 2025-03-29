package server

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/db"
	pkgsqlite "github.com/bnema/gordon/pkg/sqlite"
	"github.com/charmbracelet/log"
	"github.com/google/uuid"
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

// InitializeDB initializes the database in memory and loads data from disk if available
func InitializeDB(a *App) (*sql.DB, error) {
	log.Debug("Initializing database")
	if err := a.ensureDBDir(); err != nil {
		return nil, fmt.Errorf("failed to ensure DB directory: %w", err)
	}

	diskDBPath := a.getDiskDBFilePath()
	a.DBPath = diskDBPath

	// --- Start Refactored Schema Handling ---
	log.Debug("Ensuring database schema is up-to-date on disk", "path", diskDBPath)
	// UpdateDatabase will create the file and tables if they don't exist,
	// or update the schema if they do. It also handles backups.
	if err := pkgsqlite.UpdateDatabase(diskDBPath, a.DBTables); err != nil {
		return nil, fmt.Errorf("failed to create/update database schema on disk: %w", err)
	}
	log.Debug("Database schema on disk is up-to-date.")
	// --- End Refactored Schema Handling ---

	// --- BEGIN Seed Admin Account if Necessary ---
	log.Debug("Checking if admin account seeding is necessary", "path", diskDBPath)
	// Need to temporarily open the disk DB to check and potentially write
	seedDB, err := sql.Open("sqlite", diskDBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open disk DB for seeding check: %w", err)
	}

	var accountCount int
	err = seedDB.QueryRow("SELECT COUNT(*) FROM account").Scan(&accountCount)
	if err != nil {
		seedDB.Close()
		// If the table doesn't exist yet (shouldn't happen after UpdateDatabase), log it
		if pkgsqlite.IsNoSuchTableError(err) {
			log.Warn("Account table not found during seeding check, skipping seed.", "error", err)
			// Proceed without seeding, assuming UpdateDatabase handled creation correctly
		} else {
			return nil, fmt.Errorf("failed to query account count from disk DB: %w", err)
		}
	} else if accountCount == 0 {
		log.Info("No accounts found, seeding default admin user and account.")
		// Begin transaction for seeding
		seedTx, txErr := seedDB.Begin()
		if txErr != nil {
			seedDB.Close()
			return nil, fmt.Errorf("failed to begin transaction for seeding: %w", txErr)
		}

		adminUserID := uuid.NewString()
		adminAccountID := uuid.NewString()
		adminEmail := "admin@local.host" // Or some other placeholder

		// Insert User
		_, txErr = seedTx.Exec("INSERT INTO user (id, name, email) VALUES (?, ?, ?)",
			adminUserID, "Admin User", adminEmail)
		if txErr != nil {
			seedTx.Rollback()
			seedDB.Close()
			return nil, fmt.Errorf("failed to insert default admin user: %w", txErr)
		}

		// Insert Account
		_, txErr = seedTx.Exec("INSERT INTO account (id, user_id) VALUES (?, ?)",
			adminAccountID, adminUserID)
		if txErr != nil {
			seedTx.Rollback()
			seedDB.Close()
			return nil, fmt.Errorf("failed to insert default admin account: %w", txErr)
		}

		// Commit transaction
		txErr = seedTx.Commit()
		if txErr != nil {
			seedDB.Close()
			return nil, fmt.Errorf("failed to commit seeding transaction: %w", txErr)
		}
		log.Info("Default admin user and account seeded successfully", "user_id", adminUserID, "account_id", adminAccountID)
	} else {
		log.Debug("Accounts already exist, skipping admin seeding.", "count", accountCount)
	}

	// Close the temporary connection used for seeding
	if err := seedDB.Close(); err != nil {
		// Log error but don't necessarily fail initialization at this point
		log.Error("Error closing temporary seed DB connection", "error", err)
	}
	// --- END Seed Admin Account if Necessary ---

	// Create in-memory database connection (remains the same)
	log.Debug("Opening in-memory SQLite database")
	memDB, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		return nil, fmt.Errorf("failed to open in-memory DB: %w", err)
	}

	// Test that the in-memory database is working (remains the same)
	log.Debug("Testing in-memory database connection")
	if err := memDB.Ping(); err != nil {
		memDB.Close() // Close memDB if ping fails
		return nil, fmt.Errorf("failed to ping in-memory database: %w", err)
	}

	// --- Load from Disk to Memory ---
	// Now that the disk schema is guaranteed to be correct, load it into memory.
	// We need to check if the file actually contains data before loading.
	// A simple way is to check if the file size is > 0 after UpdateDatabase.
	fileInfo, err := os.Stat(diskDBPath)
	loadNeeded := true
	if err != nil {
		if os.IsNotExist(err) {
			log.Debug("Database file still doesn't exist after schema update (likely first run with no tables?), skipping load from disk.")
			loadNeeded = false // Nothing to load
		} else {
			memDB.Close()
			return nil, fmt.Errorf("failed to stat disk DB file after schema update: %w", err)
		}
	} else if fileInfo.Size() == 0 {
		log.Debug("Database file is empty after schema update, skipping load from disk.")
		loadNeeded = false // Nothing to load
	}

	if loadNeeded {
		log.Debug("Loading database from disk into memory", "path", diskDBPath)
		if err := loadDiskDBToMemory(diskDBPath, memDB); err != nil {
			memDB.Close()
			return nil, fmt.Errorf("failed to load disk DB into memory: %w", err)
		}
		log.Debug("Successfully loaded disk database into memory")
	} else {
		// If we didn't load, the memory DB is empty but has the correct schema
		// implicitly because UpdateDatabase already ran on the (possibly new) disk file.
		// However, loadDiskDBToMemory *also* creates the schema in memory based on the disk file.
		// If the disk file was *just* created by UpdateDatabase and is empty,
		// loadDiskDBToMemory won't run, and memDB won't have the tables yet.
		// We need to ensure the schema exists in memory even if the disk file was initially empty.
		// Easiest way: re-apply the schema creation logic to memDB.
		log.Debug("Applying schema to empty in-memory database")
		memTx, err := memDB.Begin()
		if err != nil {
			memDB.Close()
			return nil, fmt.Errorf("failed to begin transaction on memory DB for schema creation: %w", err)
		}
		schemaCreated := false
		// Iterate through DBTables fields using reflection to get correct table names
		dbTablesType := reflect.TypeOf(a.DBTables)
		dbTablesValue := reflect.ValueOf(a.DBTables)
		if dbTablesType.Kind() == reflect.Ptr {
			dbTablesType = dbTablesType.Elem()
			dbTablesValue = dbTablesValue.Elem()
		}

		if dbTablesType.Kind() == reflect.Struct {
			log.Debug("Reflecting DBTables struct for in-memory schema creation", "type", dbTablesType.Name())
			for i := 0; i < dbTablesType.NumField(); i++ {
				field := dbTablesType.Field(i)
				fieldValue := dbTablesValue.Field(i).Interface() // e.g., db.ProxyRoute instance
				sqlTag := field.Tag.Get("sql")

				if sqlTag == "" || sqlTag == "-" {
					log.Debug("Skipping field without sql tag in memory schema creation", "field", field.Name)
					continue // Skip fields without a sql tag or marked to be ignored
				}

				tableName := sqlTag // Use the tag directly as the table name
				log.Debug("Attempting to create table in memory", "tableName", tableName, "structType", reflect.TypeOf(fieldValue).Name())

				if err := pkgsqlite.CreateTableInTx(memTx, fieldValue, tableName); err != nil {
					// Ignore "table already exists" errors if somehow they occur
					if !strings.Contains(strings.ToLower(err.Error()), "already exists") {
						log.Error("Failed to create table in memory", "table", tableName, "error", err)
						memTx.Rollback()
						memDB.Close()
						return nil, fmt.Errorf("failed to create table %s in memory: %w", tableName, err)
					} else {
						log.Debug("Table already exists in memory, skipping creation", "table", tableName)
					}
				} else {
					log.Debug("Successfully created table in memory", "table", tableName)
					schemaCreated = true
				}
			}
		} else {
			log.Error("a.DBTables is not a struct, cannot create schema in memory")
			memDB.Close()
			return nil, fmt.Errorf("internal error: DBTables is not a struct type (%T)", a.DBTables)
		}

		if err := memTx.Commit(); err != nil {
			memDB.Close()
			return nil, fmt.Errorf("failed to commit schema creation transaction to memory DB: %w", err)
		}
		if schemaCreated {
			log.Debug("Successfully applied schema to in-memory database")
		} else {
			log.Debug("In-memory schema already existed or no tables defined.")
		}

	}
	// --- End Load from Disk to Memory ---

	a.DB = memDB

	// Populate struct tables if data was loaded
	if loadNeeded {
		log.Debug("Populating struct tables from loaded database")
		if err := PopulateStructWithTables(a); err != nil {
			// Don't fail initialization for this, maybe just log an error
			log.Error("Failed to populate struct tables after loading DB", "error", err)
			// Depending on severity, you might want to return nil, err here
		}
	} else {
		log.Debug("Skipping struct table population as no data was loaded from disk.")
	}

	// Create context for auto-save (remains the same)
	a.DBSaveCtx, a.DBSaveCancel = context.WithCancel(context.Background())

	// Start auto-save routine (remains the same)
	log.Debug("Starting auto-save routine with interval", "minutes", AutoSaveInterval)
	go autoSaveDB(a.DBSaveCtx, a, AutoSaveInterval)

	log.Info("Database initialization completed successfully")
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
	return rows.Scan(
		&d.ID,
		&d.Name,
		&d.AccountID,
		&d.CreatedAt,
		&d.UpdatedAt,
		&d.AcmeEnabled,
		&d.AcmeChallengeType,
		&d.AcmeDnsProvider,
		&d.AcmeDnsCredentialsRef,
	)
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
	if err := populateTableFromDB(a, "SELECT id, name, account_id, created_at, updated_at, acme_enabled, acme_challenge_type, acme_dns_provider, acme_dns_credentials_ref FROM domain", domainWrapper); err != nil {
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
