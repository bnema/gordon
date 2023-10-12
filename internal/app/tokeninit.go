package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// HandleNewInitialization checks if an admin user exists in db, and if not, generates a token for the initial login and stores it in the config file
func (a *App) HandleNewTokenInitialization() (string, error) {
	// Query to check if a user with id=1 exists
	query := `SELECT COUNT(id) FROM users WHERE id = "1"`
	var count int
	err := a.DB.QueryRow(query).Scan(&count)
	if err != nil {
		return "", fmt.Errorf("failed to check for admin user: %v", err)
	}

	// If count is 1, it means admin user already exists
	if count == 1 {
		return "", nil
	}

	// If we reach here, it means admin does not exist, so we generate a token
	token, err := generateRandomToken(16) // 16 bytes
	if err != nil {
		return "", fmt.Errorf("failed to generate token: %v", err)
	}

	fmt.Printf("Initial login token: %s\n", token)

	// Store the token in the config file
	a.Config.General.GordonToken = token
	err = a.Config.UpdateConfig()
	if err != nil {
		return "", fmt.Errorf("failed to save config: %v", err)
	}
	return token, nil
}

// generateRandomToken generates a random token of the given byte length
func generateRandomToken(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
