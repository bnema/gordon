package app

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bnema/gordon/internal/db/migrate"
	"github.com/mattn/go-sqlite3"
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
	// Here we can generate the initial checksum
	a.InitialChecksum, err = GenerateDBChecksum(memDb)
	if err != nil {
		return nil, fmt.Errorf("failed to generate initial checksum: %w", err)
	}
	// Initialize the DB field in the App struct
	a.DB = memDb

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
	if err := migrate.CreateAccountTable(db); err != nil {
		return err
	}
	if err := migrate.CreateSessionTable(db); err != nil {
		return err
	}

	if err := migrate.CreateProviderTable(db); err != nil {
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

	diskDb, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}
	defer diskDb.Close()

	// Get a single connection from each *sql.DB
	memConn, err := memDb.Conn(context.Background())
	if err != nil {
		return nil, err
	}
	defer memConn.Close()

	diskConn, err := diskDb.Conn(context.Background())
	if err != nil {
		return nil, err
	}
	defer diskConn.Close()

	err = memConn.Raw(func(driverConn interface{}) error {
		memSqliteConn, ok := driverConn.(*sqlite3.SQLiteConn)
		if !ok {
			return fmt.Errorf("failed to cast to *sqlite3.SQLiteConn")
		}

		return diskConn.Raw(func(driverConn interface{}) error {
			diskSqliteConn, ok := driverConn.(*sqlite3.SQLiteConn)
			if !ok {
				return fmt.Errorf("failed to cast to *sqlite3.SQLiteConn")
			}

			backup, err := memSqliteConn.Backup("main", diskSqliteConn, "main")
			if err != nil {
				return err
			}
			defer backup.Close()

			done, err := backup.Step(-1)
			if err != nil {
				return err
			}
			if !done {
				return fmt.Errorf("backup not fully completed")
			}

			return backup.Finish()
		})
	})

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
