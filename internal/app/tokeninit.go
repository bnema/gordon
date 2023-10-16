package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// HandleNewInitialization checks if there is ANY user in the database.
func HandleNewTokenInitialization(a *App) (string, error) {
	// Check if there is any user in the database
	query := "SELECT COUNT(*) FROM user"
	var count int
	err := a.DB.QueryRow(query).Scan(&count)
	if err != nil {
		return "", fmt.Errorf("failed to check for admin user: %v", err)
	}

	// If count is greater than 0, it means there is at least one user in the database
	if count > 0 {
		return "", fmt.Errorf("user already present, skipping token initialization")
	}

	// If we reach here, it means admin does not exist, so we generate a token
	token, err := generateRandomToken(16) // 16 bytes
	if err != nil {
		return "", fmt.Errorf("failed to generate token: %v", err)
	}

	// url = config.Http.SubDomain + config.Http.TopDomain + config.Http.Port + config.Admin.Path

	fmt.Printf("Login with the new token: %s\n", token)

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
