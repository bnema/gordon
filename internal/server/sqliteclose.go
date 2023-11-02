package server

// CloseAndSaveDB closes the in-memory database and backup the database file if there is any modification.
func CloseDB(a *App) error {
	if a.DB != nil {
		saveDB(a)
		a.DB.Close()
	}
	return nil
}

func saveDB(a *App) error {

	return nil
}

// TODO
func backupDBFile(a *App) error {
	// Create a backup of the database file at the same location as the database file.
	// The backup file name is the database file name with a timestamp appended to it.
	// If the backup file already exists, overwrite it.
	return nil
}
