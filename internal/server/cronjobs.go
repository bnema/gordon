package server

import (
	"log"
	"time"
)

// SessionCleaner, time as a string
func SessionCleaner(a *App, currentTime string) error {
	query := "DELETE FROM sessions WHERE expires < ?"
	_, err := a.DB.Exec(query, currentTime)
	if err != nil {
		return err
	}

	return nil
}

// StartSessionCleaner initializes the session cleaner to run at regular intervals.
func (a *App) StartSessionCleaner() {
	go func() {
		for {
			currentTime := time.Now().Format(time.RFC3339)
			if err := SessionCleaner(a, currentTime); err != nil {
				log.Printf("Failed to clean up sessions: %v", err)
			}
			time.Sleep(1 * time.Hour)
		}
	}()
}
