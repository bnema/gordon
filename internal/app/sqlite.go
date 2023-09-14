package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// Initialize in-memory SQLite database and returns its connection
func InitDB() (db *sql.DB, err error) {
	db, err = sql.Open("sqlite3", "file::memory?cache=shared")
	if err != nil {
		return nil, fmt.Errorf("failed to open in-memory database: %v", err)
	}
	return
}

// CloseDB closes the database connection and backs up the in-memory database to disk
func CloseDB(db *sql.DB, a *App) error {
	if err := BackupDB(db, a); err != nil {
		return err
	}

	if err := db.Close(); err != nil {
		return fmt.Errorf("failed to close database: %v", err)
	}

	return nil
}

// LoadDBIntoMemory loads the database from disk into memory
func LoadDBIntoMemory(a *App) (*sql.DB, error) {
	dbfp := a.GetDBFilePath()

	if _, err := os.Stat(dbfp); err != nil {
		return nil, nil
	}

	memDB, err := InitDB()
	if err != nil {
		return nil, err
	}

	diskDB, err := sql.Open("sqlite3", dbfp)
	if err != nil {
		return nil, fmt.Errorf("failed to open database file: %v", err)
	}

	if err := transferData(diskDB, memDB); err != nil {
		return nil, err
	}

	return memDB, nil
}

// transferData transfers data from srcDB to dstDB
func transferData(srcDB, dstDB *sql.DB) error {
	srcConn, err := srcDB.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get connection from srcDB: %v", err)
	}
	defer srcConn.Close()

	dstConn, err := dstDB.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get connection from dstDB: %v", err)
	}
	defer dstConn.Close()

	return srcConn.Raw(func(srcRawConn interface{}) error {
		return dstConn.Raw(func(dstRawConn interface{}) error {
			srcSqlite3Conn := srcRawConn.(*sqlite3.SQLiteConn)
			dstSqlite3Conn := dstRawConn.(*sqlite3.SQLiteConn)

			_, err := srcSqlite3Conn.Backup("main", dstSqlite3Conn, "main")
			return err
		})
	})
}

// BackupDB backs up the in-memory database to disk
func BackupDB(db *sql.DB, a *App) error {
	dbfp := a.GetDBFilePath()

	conn, err := db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get underlying database connection: %v", err)
	}

	return conn.Raw(func(sqliteConn interface{}) error {
		sqlite3Conn := sqliteConn.(*sqlite3.SQLiteConn)
		backupDB, err := sqlite3Conn.Backup(dbfp, sqlite3Conn, "main")
		if err != nil {
			return fmt.Errorf("failed to create backup: %v", err)
		}
		defer backupDB.Close()

		_, err = backupDB.Step(-1)
		if err != nil {
			return fmt.Errorf("failed to backup in-memory database: %v", err)
		}

		return nil
	})
}
