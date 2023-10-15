package app

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	pkgsqlite "github.com/bnema/gordon/pkg/sqlite"
	_ "github.com/mattn/go-sqlite3"
)

const (
	DBDir      = "tmp/data"
	DBFilename = "sqlite.db"
)

func (a *App) getDiskDBFilePath() string {
	DiskDBfilepath := filepath.Join(a.DBDir, a.DBFilename)
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

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open disk DB: %w", err)
	}

	a.DB = db

	return db, nil
}

func bootstrapDB(dbPath string, app *App) error {
	db, err := sql.Open("sqlite3", dbPath)
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

	return nil
}
