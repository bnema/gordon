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
	// TODO: Backup the database file if there is any modification.
	return nil
}
