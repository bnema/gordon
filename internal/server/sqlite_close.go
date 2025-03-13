package server

import (
	"fmt"

	"github.com/charmbracelet/log"
)

// CloseDB saves the in-memory database to disk and then closes the database connection.
func CloseDB(a *App) error {
	log.Debug("CloseDB called, checking if database connection exists")

	if a.DB != nil {
		log.Debug("Database connection exists, proceeding with cleanup")

		// Cancel the auto-save routine
		if a.DBSaveCancel != nil {
			log.Debug("Canceling auto-save routine")
			a.DBSaveCancel()
		} else {
			log.Debug("No auto-save routine to cancel")
		}

		log.Info("Saving in-memory database to disk before shutdown")

		// Save the in-memory database to disk
		if err := saveMemDBToDisk(a.DB, a.DBPath); err != nil {
			log.Error("Failed to save in-memory database to disk during shutdown", "error", err)
			// Continue with closing even if saving fails
		} else {
			log.Info("Successfully saved in-memory database to disk")
		}

		// Close the database connection
		log.Debug("Closing database connection")
		if err := a.DB.Close(); err != nil {
			log.Error("Failed to close database connection", "error", err)
			return fmt.Errorf("failed to close database: %w", err)
		}

		log.Info("Database connection closed successfully")
		// Set DB to nil to prevent double-close attempts
		a.DB = nil
	} else {
		log.Debug("No database connection to close")
	}

	return nil
}

// func saveDB(a *App) error {

// 	return nil
// }

// // TODO
// func backupDBFile(a *App) error {
// 	// Create a backup of the database file at the same location as the database file.
// 	// The backup file name is the database file name with a timestamp appended to it.
// 	// If the backup file already exists, overwrite it.
// 	return nil
// }
