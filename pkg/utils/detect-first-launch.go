package utils

import (
	"os"
)

// IsConfigFilePresent checks if it's the first launch of the app based on the existence of gordon.yml
func IsConfigFilePresent() bool {
	// Check if gordon.yml exists at the root of the project
	if _, err := os.Stat("gordon.yml"); os.IsNotExist(err) {
		return true
	}
	return false
}
