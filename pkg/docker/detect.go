package docker

import (
	"os"
)

func fileExists(filepath string) bool {
	_, err := os.Stat(filepath)
	return !os.IsNotExist(err)
}

func IsRunningInContainer() bool {
	// Check for .dockerenv file
	return fileExists("/.iscontainer") || fileExists("/.dockerenv")
}
