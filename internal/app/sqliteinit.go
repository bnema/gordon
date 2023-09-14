package app

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bnema/gordon/internal/database/migrate"
	_ "github.com/mattn/go-sqlite3"
)

const (
	DBDir      = "tmp/data"
	DBFilename = "sqlite.db"
)

func (a *App) GetDiskDBFilePath() string {
	DiskDBfilepath := filepath.Join(a.DBDir, a.DBFilename)
	return DiskDBfilepath
}

// InitializeDB initializes the database
func InitializeDB(a *App) (*sql.DB, error) {
	if err := a.ensureDBDir(); err != nil {
		return nil, fmt.Errorf("failed to ensure DB directory: %w", err)
	}

	dbPath := a.GetDiskDBFilePath()

	dbExists, err := a.checkDBFile(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to check DB file: %w", err)
	}

	if !dbExists {
		if err := bootstrapDB(dbPath); err != nil {
			return nil, fmt.Errorf("failed to bootstrap DB: %w", err)
		}
	}

	memDb, err := loadDBToMemory(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load DB to memory: %w", err)
	}

	return memDb, nil
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

// bootstrapDB executes the SQL statements to create the database tables.
func bootstrapDB(dbPath string) error {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := migrate.CreateUserTable(db); err != nil {
		return err
	}

	return nil
}

// loadDBToMemory loads the database from disk to memory.
func loadDBToMemory(dbPath string) (*sql.DB, error) {
	memDb, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return nil, err
	}

	_, err = memDb.Exec("ATTACH DATABASE '" + dbPath + "' AS diskdb")
	if err != nil {
		return nil, err
	}

	_, err = memDb.Exec("CREATE TABLE users AS SELECT * FROM diskdb.users")
	if err != nil {
		return nil, err
	}

	_, err = memDb.Exec("DETACH DATABASE diskdb")
	if err != nil {
		return nil, err
	}

	return memDb, nil
}

// GenerateDBChecksum generates a checksum of the database.
func GenerateDBChecksum(db *sql.DB) (string, error) {
	tables, err := GetTableNames(db)
	if err != nil {
		return "", err
	}

	var finalChecksum []byte

	for _, table := range tables {
		hasher := md5.New() // Create a new hasher for each table

		rows, err := db.Query("SELECT * FROM " + table)
		if err != nil {
			return "", err
		}

		for rows.Next() {
			cols, err := rows.Columns()
			if err != nil {
				return "", err
			}

			vals := make([]interface{}, len(cols))
			for i := range cols {
				vals[i] = new(sql.RawBytes)
			}

			err = rows.Scan(vals...)
			if err != nil {
				return "", err
			}

			for i := range vals {
				hasher.Write(*vals[i].(*sql.RawBytes))
			}
		}

		if err := rows.Err(); err != nil {
			rows.Close()
			return "", err
		}

		rows.Close()

		finalChecksum = append(finalChecksum, hasher.Sum(nil)...)
	}

	return hex.EncodeToString(finalChecksum), nil
}
