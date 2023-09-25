package store

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/pkg/utils/docker"
	"github.com/docker/docker/api/types"
)

type StorageConfig struct {
	StorageDir string
	ImageID    string
	Images     []types.ImageSummary
}

func NewStorageConfig(config *app.AppConfig) *StorageConfig {
	return &StorageConfig{
		StorageDir: config.General.StorageDir,
	}
}

func SaveImageToStorage(config *app.AppConfig, filename string, buf io.Reader) (string, error) {
	// Check if the folder exist if not create it
	if _, err := os.Stat(config.General.StorageDir); os.IsNotExist(err) {
		err := os.MkdirAll(config.General.StorageDir, 0755)
		if err != nil {
			return "", fmt.Errorf("failed to create storage directory: %v", err)
		}
	}

	// Define the path where the image will be saved
	saveInPath := filepath.Join(config.General.StorageDir, filename)

	// Create or open a file for appending.
	outFile, err := os.Create(saveInPath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %v", err)
	}

	// Write the uploaded file's content to the outFile
	_, err = io.Copy(outFile, buf)
	if err != nil {
		return "", fmt.Errorf("failed to write file: %v", err)
	}

	// Check if file exists
	if _, err := os.Stat(saveInPath); os.IsNotExist(err) {
		return "", fmt.Errorf("file does not exist: %v", err)
	}

	// Import the image into Docker
	imageId, err := docker.ImportImageToEngine(saveInPath)
	if err != nil {
		return "", fmt.Errorf("failed to import image to Docker: %v", err)
	}

	return imageId, nil
}

func (sc *StorageConfig) DeleteImageFromStorage(imageId string) error {
	// Delete the image from the storage directory
	imagePath := filepath.Join(sc.StorageDir, imageId+".tar")
	err := os.Remove(imagePath)
	if err != nil {
		return fmt.Errorf("failed to delete image from storage directory: %v", err)
	}

	return nil
}
