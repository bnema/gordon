package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bnema/gordon/internal/database/migrate"
	"github.com/mattn/go-sqlite3"
)

// InitializeDB initializes the SQLite database. If a database file exists on disk, it loads it into memory.
// Otherwise, it creates a new in-memory database and bootstraps it.
func InitializeDB(a *App) (*sql.DB, error) {
	dbfp := a.GetDBFilePath()

	// Check if the directory exists, if not create it
	dir := filepath.Dir(a.DBDir)
	fmt.Println("dir", dir)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		err = os.MkdirAll(dir, 0755)
		if err != nil {
			return nil, fmt.Errorf("failed to create directory: %v", err)
		}
	}

	// If the database file doesn't exist on disk
	if _, err := os.Stat(dbfp); os.IsNotExist(err) {
		memDB, err := initInMemoryDB()
		if err != nil {
			return nil, err
		}

		// Run the bootstrap
		RunBootstrap(memDB)

		// Explicitly backup the in-memory database to disk
		err = BackupDB(memDB, a)
		if err != nil {
			return nil, fmt.Errorf("failed to backup in-memory database to disk: %v", err)
		}
	}

	memDB, err := initInMemoryDB()
	if err != nil {
		return nil, err
	}
	diskDB, err := sql.Open("sqlite3", dbfp)
	if err != nil {
		return nil, fmt.Errorf("failed to open database file: %v", err)
	}
	defer diskDB.Close()

	if err := transferData(diskDB, memDB); err != nil {
		return nil, err
	}
	return memDB, nil
}

func initInMemoryDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite3", "file::memory?cache=shared")
	if err != nil {
		return nil, fmt.Errorf("failed to open in-memory database: %v", err)
	}
	return db, nil
}

func CloseAndBackupDB(db *sql.DB, a *App) error {
	if err := BackupDB(db, a); err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("failed to close database: %v", err)
	}
	return nil
}

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

func BackupDB(db *sql.DB, a *App) error {
	dbfp := a.GetDBFilePath()
	conn, err := db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get underlying database connection: %v", err)
	}
	defer conn.Close()

	return conn.Raw(func(sqliteConn interface{}) error {
		sqlite3Conn := sqliteConn.(*sqlite3.SQLiteConn)
		diskDB, err := sql.Open("sqlite3", dbfp)
		fmt.Println("diskDB", diskDB)
		if err != nil {
			return fmt.Errorf("failed to open destination database file: %v", err)
		}
		defer diskDB.Close()

		diskConn, err := diskDB.Conn(context.Background())
		if err != nil {
			return fmt.Errorf("failed to get connection from diskDB: %v", err)
		}
		defer diskConn.Close()

		var backupDB *sqlite3.SQLiteBackup
		err = diskConn.Raw(func(diskRawConn interface{}) error {
			backupDB, err = sqlite3Conn.Backup("main", diskRawConn.(*sqlite3.SQLiteConn), "main")
			return err
		})
		defer backupDB.Close()

		_, err = backupDB.Step(-1)
		if err != nil {
			return fmt.Errorf("failed to backup in-memory database: %v", err)
		}
		return nil
	})
}

func RunBootstrap(db *sql.DB) error {
	return migrate.CreateUserTable(db)
}
