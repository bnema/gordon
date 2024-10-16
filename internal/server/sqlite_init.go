package server

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bnema/gordon/internal/db"
	pkgsqlite "github.com/bnema/gordon/pkg/sqlite"
	_ "modernc.org/sqlite"
)

const (
	DBFilename = "sqlite.db"
)

func (a *App) getDiskDBFilePath() string {
	DiskDBfilepath := filepath.Join(a.DBDir, a.DBFilename)
	// if a.DBDir is empty return log "Set a path using storageDir in the config file"
	return DiskDBfilepath
}

// ensureDBDir ensures that the database directory exists.
func (a *App) ensureDBDir() error {
	if _, err := os.Stat(a.DBDir); os.IsNotExist(err) {
		if err := os.MkdirAll(a.DBDir, 0755); err != nil {
			return err
		}
	}
	return nil
}

// checkDBFile checks if the database file exists.
func (a *App) checkDBFile(dbPath string) (bool, error) {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

// InitializeDB initializes the database
func InitializeDB(a *App) (*sql.DB, error) {
	if err := a.ensureDBDir(); err != nil {
		return nil, fmt.Errorf("failed to ensure DB directory: %w", err)
	}

	dbPath := a.getDiskDBFilePath()

	dbExists, err := a.checkDBFile(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to check DB file: %w", err)
	}

	if !dbExists {
		if err := bootstrapDB(dbPath, a); err != nil {
			return nil, fmt.Errorf("failed to bootstrap DB: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open disk DB: %w", err)
	}

	a.DB = db

	if dbExists {
		// Populate the struct tables from the database
		if err := PopulateStructWithTables(a); err != nil {
			return nil, err
		}
	}

	return db, nil
}

func bootstrapDB(dbPath string, app *App) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	tables := app.DBTables

	if err := pkgsqlite.CreateTable(db, tables.User, "user"); err != nil {
		return err
	}
	if err := pkgsqlite.CreateTable(db, tables.Account, "account"); err != nil {
		return err
	}
	if err := pkgsqlite.CreateTable(db, tables.Sessions, "sessions"); err != nil {
		return err
	}
	if err := pkgsqlite.CreateTable(db, tables.Provider, "provider"); err != nil {
		return err
	}

	if err := pkgsqlite.CreateTable(db, tables.Clients, "clients"); err != nil {
		return err
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

// PopulateStructWithTables updates db struct with tables
func PopulateStructWithTables(a *App) error {
	tables := map[string]DBTable{
		"SELECT id, name, email FROM user":                                                    &UserWrapper{&a.DBTables.User},
		"SELECT id, user_id FROM account":                                                     &AccountWrapper{&a.DBTables.Account},
		"SELECT id, account_id, name, login, avatar_url, profile_url, email FROM provider":    &ProviderWrapper{&a.DBTables.Provider},
		"SELECT id, account_id, access_token, browser_info, expires, is_online FROM sessions": &SessionsWrapper{&a.DBTables.Sessions},
		"SELECT id, account_id, os, ip, hostname, expires FROM clients":                       &ClientsWrapper{&a.DBTables.Clients},
	}

	for query, table := range tables {
		if err := populateTableFromDB(a, query, table); err != nil {
			return err
		}
	}

	return nil
}
