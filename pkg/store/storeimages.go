package store

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/bnema/gordon/internal/common"
	"github.com/google/uuid"
)

var (
	imagePath string
	mu        sync.Mutex
)

// setImagePath sets the image path in a thread-safe manner
func setImagePath(path string) {
	mu.Lock()
	defer mu.Unlock()
	imagePath = path
}

// getImagePath gets the image path in a thread-safe manner
func getImagePath() string {
	mu.Lock()
	defer mu.Unlock()
	return imagePath
}

func SaveImageToStorage(config *common.Config, originalFilename string, buf io.Reader) (string, error) {
	// Ensure the images directory exists
	imagesDir := filepath.Join(config.General.StorageDir, "images")
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create images directory: %v", err)
	}

	// Generate a unique ID for the image
	id := uuid.New().String()

	// Create a filename with the ID and original extension
	ext := filepath.Ext(originalFilename)
	filename := fmt.Sprintf("%s%s", id, ext)

	// Full path for the new image
	imagePath := filepath.Join(imagesDir, filename)

	fmt.Printf("Saving image to: %s\n", imagePath)

	// Create the file
	outFile, err := os.Create(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to create file: %v", err)
	}
	defer outFile.Close()

	// Write the content
	written, err := io.Copy(outFile, buf)
	if err != nil {
		os.Remove(imagePath) // Clean up in case of error
		return "", fmt.Errorf("failed to write file: %v", err)
	}

	fmt.Printf("Wrote %d bytes to %s\n", written, imagePath)

	return imagePath, nil
}

func RemoveFromStorage(imagePath string) error {
	if imagePath == "" {
		return fmt.Errorf("no image path provided to remove")
	}

	fmt.Printf("Attempting to remove file: %s\n", imagePath)

	err := os.Remove(imagePath)
	if err != nil {
		return fmt.Errorf("failed to remove file: %v", err)
	}

	fmt.Printf("Successfully removed file: %s\n", imagePath)

	return nil
}
