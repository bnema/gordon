package server

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bnema/gordon/internal/db"
	"github.com/charmbracelet/log"
	_ "modernc.org/sqlite"
)

const (
	DBFilename = "sqlite.db"
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

// InitializeDB initializes the database
func InitializeDB(a *App) (*sql.DB, error) {
	log.Debug("Initializing database")
	if err := a.ensureDBDir(); err != nil {
		return nil, fmt.Errorf("failed to ensure DB directory: %w", err)
	}

	dbPath := a.getDiskDBFilePath()
	a.DBPath = dbPath

	dbExists, err := a.checkDBFile(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to check DB file: %w", err)
	}

	if !dbExists {
		log.Debug("Database needs bootstrapping", "path", dbPath)
		if err := bootstrapDB(dbPath); err != nil {
			return nil, fmt.Errorf("failed to bootstrap DB: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open disk DB: %w", err)
	}

	// Test that the database is working
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	a.DB = db

	if dbExists {
		// Populate the struct tables from the database
		log.Debug("Populating struct tables from existing database")
		if err := PopulateStructWithTables(a); err != nil {
			return nil, err
		}
	} else {
		log.Debug("Initialized new database")
	}

	return db, nil
}

func bootstrapDB(dbPath string) error {
	// Open the database file
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("error opening database: %w", err)
	}
	defer db.Close()

	// Begin a transaction
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("error beginning transaction: %w", err)
	}

	// Create tables
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
		return fmt.Errorf("failed to create tables: %w", err)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

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
	return rows.Scan(&p.ID, &p.DomainID, &p.ContainerID, &p.ContainerIP, &p.ContainerPort, &p.Protocol, &p.Path, &p.Active, &p.CreatedAt, &p.UpdatedAt)
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
	if err := populateTableFromDB(a, "SELECT id, domain_id, container_id, container_ip, container_port, protocol, path, active, created_at, updated_at FROM proxy_route", proxyRouteWrapper); err != nil {
		return err
	}

	return nil
}
